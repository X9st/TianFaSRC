package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/X9st/TianFaSRC/internal/auth"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/store/postgres"
	"github.com/X9st/TianFaSRC/internal/system"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase1Flow(t *testing.T) {
	base := os.Getenv("TIANFA_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TIANFA_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := "tianfa_api_" + strings.ReplaceAll(identity.NewID(), "-", "")[16:]
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+name) })
	poolConfig, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	authStore := auth.Store{Pool: pool}
	_, err = authStore.InitAdmin(ctx, "admin@example.invalid", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", Readiness: system.Readiness{Database: pool, DataDir: t.TempDir()}, Pool: pool})
	call := func(method, path string, body any, cookies []*http.Cookie, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		var input io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(b)
		}
		r := httptest.NewRequest(method, path, input)
		r.Host = "localhost"
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	must := func(rec *httptest.ResponseRecorder, want int) {
		t.Helper()
		if rec.Code != want {
			t.Fatalf("status %d want %d: %s", rec.Code, want, rec.Body.String())
		}
	}
	must(call("GET", "/api/v1/tasks", nil, nil, ""), 401)
	insecure := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewBufferString(`{"email":"admin@example.invalid","password":"correct horse battery staple"}`))
	insecure.Host = "tianfa.example"
	insecureRecorder := httptest.NewRecorder()
	h.ServeHTTP(insecureRecorder, insecure)
	must(insecureRecorder, 403)
	login := call("POST", "/api/v1/auth/login", map[string]any{"email": "admin@example.invalid", "password": "correct horse battery staple"}, nil, "")
	must(login, 200)
	cookies := login.Result().Cookies()
	var csrf string
	for _, c := range cookies {
		if c.Name == csrfCookie {
			csrf = c.Value
		}
	}
	if csrf == "" {
		t.Fatal("missing csrf cookie")
	}
	input := map[string]any{"title": "授权测试", "description": "仅测试范围内系统", "goals": []any{map[string]any{"text": "验证授权访问", "priority": 8}}, "scope": map[string]any{"entries": []any{map[string]any{"effect": "allow", "kind": "domain", "value": "EXAMPLE.INVALID", "ports": []int{443}}}}, "constraints": []any{map[string]any{"effect": "deny", "text": "禁止压测"}}, "execution": map[string]any{"policy_mode": "autonomous", "worker_limit": 3, "budget": map[string]any{"max_input_tokens": 1000}}}
	must(call("POST", "/api/v1/tasks", input, cookies, ""), 403)
	created := call("POST", "/api/v1/tasks", input, cookies, csrf)
	must(created, 201)
	var data struct {
		Data struct {
			Task  task.Task         `json:"task"`
			Goals []task.Goal       `json:"goals"`
			Scope []task.ScopeEntry `json:"scope"`
		}
	}
	if err := json.Unmarshal(created.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	id := data.Data.Task.ID
	if id == "" || len(data.Data.Scope) != 1 || data.Data.Scope[0].NormalizedValue != "example.invalid" {
		t.Fatalf("unexpected task: %+v", data)
	}
	var budget map[string]any
	if err := json.Unmarshal(data.Data.Task.Budget, &budget); err != nil || budget["schema_version"] != float64(1) {
		t.Fatalf("unversioned budget: %s %v", data.Data.Task.Budget, err)
	}
	must(call("POST", "/api/v1/tasks/"+id+":start", map[string]any{"version": 1}, cookies, csrf), 409)
	must(call("PATCH", "/api/v1/tasks/"+id, map[string]any{"title": "已修改", "version": 1}, cookies, csrf), 200)
	must(call("PATCH", "/api/v1/tasks/"+id, map[string]any{"title": "旧版本", "version": 1}, cookies, csrf), 409)
	must(call("POST", "/api/v1/tasks/"+id+"/goals", map[string]any{"version": 2, "text": "验证另一个目标", "priority": 5}, cookies, csrf), 201)
	must(call("POST", "/api/v1/tasks/"+id+"/constraints", map[string]any{"version": 3, "effect": "deny", "text": "禁止修改配置"}, cookies, csrf), 201)
	must(call("PUT", "/api/v1/tasks/"+id+"/scope", map[string]any{"version": 4, "entries": []any{map[string]any{"effect": "allow", "kind": "domain", "value": "example.invalid"}, map[string]any{"effect": "deny", "kind": "domain", "value": "private.example.invalid"}}}, cookies, csrf), 200)
	must(call("POST", "/api/v1/tasks/"+id+"/scope:validate", map[string]any{"entries": []any{map[string]any{"effect": "allow", "kind": "url_prefix", "value": "https://example.invalid/app/"}}}, cookies, csrf), 200)
	must(call("GET", "/api/v1/tasks/"+id+"/graph", nil, cookies, ""), 200)
	company := call("POST", "/api/v1/assets", map[string]any{"kind": "company", "value": "Authorized Lab"}, cookies, csrf)
	must(company, 201)
	var parent struct{ Data task.Asset }
	if err := json.Unmarshal(company.Body.Bytes(), &parent); err != nil {
		t.Fatal(err)
	}
	assetBody := map[string]any{"kind": "root_domain", "value": "EXAMPLE.INVALID.", "parent_id": parent.Data.ID, "source_task_id": id}
	first := call("POST", "/api/v1/assets", assetBody, cookies, csrf)
	must(first, 201)
	second := call("POST", "/api/v1/assets", assetBody, cookies, csrf)
	must(second, 201)
	var one, two struct{ Data task.Asset }
	_ = json.Unmarshal(first.Body.Bytes(), &one)
	_ = json.Unmarshal(second.Body.Bytes(), &two)
	if one.Data.ID == "" || one.Data.ID != two.Data.ID {
		t.Fatal("asset dedup failed")
	}
	if len(data.Data.Goals) != 1 {
		t.Fatal("missing goal")
	}
	must(call("POST", "/api/v1/nodes/"+data.Data.Goals[0].ID+"/assets", map[string]any{"asset_id": one.Data.ID}, cookies, csrf), 200)
	assetGraph := call("GET", "/api/v1/assets/graph", nil, cookies, "")
	must(assetGraph, 200)
	var assetGraphData struct{ Data task.AssetGraph }
	if err := json.Unmarshal(assetGraph.Body.Bytes(), &assetGraphData); err != nil {
		t.Fatal(err)
	}
	if len(assetGraphData.Data.Nodes) != 2 || len(assetGraphData.Data.Edges) != 1 {
		t.Fatalf("asset graph %+v", assetGraphData.Data)
	}
	coverage := call("GET", "/api/v1/tasks/"+id+"/coverage", nil, cookies, "")
	must(coverage, 200)
	var cov struct{ Data task.Coverage }
	_ = json.Unmarshal(coverage.Body.Bytes(), &cov)
	if cov.Data.AssetCount != 1 || cov.Data.AnchoredAssets != 1 || cov.Data.GoalCount != 2 {
		t.Fatalf("unexpected coverage %+v", cov.Data)
	}
	store := task.Store{Pool: pool}
	if n, err := store.ProjectActivities(ctx, 100); err != nil || n == 0 {
		t.Fatalf("project %d %v", n, err)
	}
	if n, err := store.ProjectActivities(ctx, 100); err != nil || n != 0 {
		t.Fatalf("duplicate projection %d %v", n, err)
	}
	must(call("GET", "/api/v1/tasks/"+id+"/activities", nil, cookies, ""), 200)
	viewer, err := authStore.CreateUser(ctx, data.Data.Task.OrganizationID, data.Data.Task.CreatedBy, "viewer@example.invalid", "correct horse battery staple", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	_ = viewer
	viewerLogin := call("POST", "/api/v1/auth/login", map[string]any{"email": "viewer@example.invalid", "password": "correct horse battery staple"}, nil, "")
	must(viewerLogin, 200)
	viewerCookies := viewerLogin.Result().Cookies()
	var viewerCSRF string
	for _, c := range viewerCookies {
		if c.Name == csrfCookie {
			viewerCSRF = c.Value
		}
	}
	must(call("POST", "/api/v1/tasks", input, viewerCookies, viewerCSRF), 403)
	must(call("GET", "/api/v1/tasks/"+id, nil, viewerCookies, ""), 200)
	must(call("GET", fmt.Sprintf("/api/v1/tasks/%s", identity.NewID()), nil, viewerCookies, ""), 404)
	must(call("PATCH", "/api/v1/users/"+data.Data.Task.CreatedBy, map[string]any{"status": "disabled"}, cookies, csrf), 409)
	must(call("PATCH", "/api/v1/users/"+viewer.ID, map[string]any{"status": "disabled"}, cookies, csrf), 200)
	must(call("GET", "/api/v1/tasks", nil, viewerCookies, ""), 401)
}
