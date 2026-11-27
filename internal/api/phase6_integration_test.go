package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/X9st/TianFaSRC/internal/agent"
	"github.com/X9st/TianFaSRC/internal/auth"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/X9st/TianFaSRC/internal/secret"
	"github.com/X9st/TianFaSRC/internal/store/postgres"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/tool"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase6ConversationWorkspaceAndAudit(t *testing.T) {
	base := os.Getenv("TIANFA_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TIANFA_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := "tianfa_p6api_" + strings.ReplaceAll(identity.NewID(), "-", "")[16:]
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+name) })
	config, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	user, err := (auth.Store{Pool: pool}).InitAdmin(ctx, "phase6-admin@example.invalid", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	_, err = (auth.Store{Pool: pool}).CreateUser(ctx, user.OrganizationID, user.ID, "phase6-viewer@example.invalid", "another correct horse battery", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	secrets, err := secret.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	profiles := llm.Store{Pool: pool, Secrets: secrets}
	scheduler := &agent.Scheduler{Engine: agent.Engine{Pool: pool, Profiles: profiles}, Tools: &tool.Dispatcher{Pool: pool, DataDir: dataDir}}
	handler := NewHandler(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Pool: pool, Secrets: &secrets, Scheduler: scheduler})
	call := func(method, path string, body any, cookies []*http.Cookie, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		var input io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			input = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, input)
		req.Host = "localhost"
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	login := func(email, password string) ([]*http.Cookie, string) {
		out := call("POST", "/api/v1/auth/login", map[string]any{"email": email, "password": password}, nil, "")
		if out.Code != 200 {
			t.Fatal(out.Body.String())
		}
		cookies := out.Result().Cookies()
		for _, c := range cookies {
			if c.Name == csrfCookie {
				return cookies, c.Value
			}
		}
		t.Fatal("csrf missing")
		return nil, ""
	}
	adminCookies, csrf := login("phase6-admin@example.invalid", "correct horse battery staple")
	viewerCookies, _ := login("phase6-viewer@example.invalid", "another correct horse battery")
	item, err := (task.Store{Pool: pool}).Create(ctx, user.OrganizationID, user.ID, task.CreateInput{Title: "验收任务", Goals: []task.GoalInput{{Text: "验证首页", Priority: 5}}, Scope: struct {
		Entries []task.ScopeEntryInput `json:"entries"`
	}{Entries: []task.ScopeEntryInput{{Effect: "allow", Kind: "domain", Value: "example.invalid"}}}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/tasks/" + item.Task.ID
	send := func(action, text, goalID, intentID, criteria string) *httptest.ResponseRecorder {
		return call("POST", path+"/conversation", map[string]string{"action": action, "text": text, "goal_id": goalID, "intent_id": intentID, "success_criteria": criteria}, adminCookies, csrf)
	}
	if out := send("ask", "目前如何？", "", "", ""); out.Code != 201 || !strings.Contains(out.Body.String(), "当前任务状态") {
		t.Fatalf("ask: %d %s", out.Code, out.Body.String())
	}
	if out := call("GET", path+"/conversation", nil, adminCookies, ""); out.Code != 200 || strings.Index(out.Body.String(), "目前如何？") > strings.Index(out.Body.String(), "当前任务状态") {
		t.Fatalf("conversation order: %d %s", out.Code, out.Body.String())
	}
	if out := send("constraint", "禁止压力测试", "", "", ""); out.Code != 201 {
		t.Fatalf("constraint: %d %s", out.Code, out.Body.String())
	}
	if out := send("goal", "验证授权用户", "", "", ""); out.Code != 201 {
		t.Fatalf("goal: %d %s", out.Code, out.Body.String())
	}
	if _, err = pool.Exec(ctx, `UPDATE tasks SET status='running' WHERE id=$1`, item.Task.ID); err != nil {
		t.Fatal(err)
	}
	if out:=send("goal","运行中追加的目标","","","");out.Code!=201{t.Fatalf("running goal: %d %s",out.Code,out.Body.String())}
	var wake bool;if err=pool.QueryRow(ctx,`SELECT pending FROM planner_wakes WHERE task_id=$1`,item.Task.ID).Scan(&wake);err!=nil||!wake{t.Fatalf("planner wake: %v %v",wake,err)}
	if out := send("intent", "检查首页访问控制", item.Goals[0].ID, "", "受控响应确认访问控制结果"); out.Code != 201 {
		t.Fatalf("intent: %d %s", out.Code, out.Body.String())
	}
	var intentID string
	if err = pool.QueryRow(ctx, `SELECT id FROM task_intents WHERE task_id=$1 AND objective='检查首页访问控制'`, item.Task.ID).Scan(&intentID); err != nil {
		t.Fatal(err)
	}
	if out := send("steer", "先验证首页", "", intentID, ""); out.Code != 201 {
		t.Fatalf("steer: %d %s", out.Code, out.Body.String())
	}
	if out := send("constraint", "禁止修改数据", "", "", ""); out.Code != 409 {
		t.Fatalf("running constraint should conflict: %d", out.Code)
	}
	fileDir := filepath.Join(dataDir, "workspaces", item.Task.ID, intentID)
	if err = os.MkdirAll(fileDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(fileDir, "note.txt"), []byte("verified note"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := call("GET", path+"/workspace", nil, adminCookies, ""); out.Code != 200 || !strings.Contains(out.Body.String(), "note.txt") {
		t.Fatalf("files: %d %s", out.Code, out.Body.String())
	}
	if out := call("GET", path+"/workspace/"+intentID+"/note.txt", nil, adminCookies, ""); out.Code != 200 || !strings.Contains(out.Body.String(), "verified note") {
		t.Fatalf("preview: %d %s", out.Code, out.Body.String())
	}
	if out := call("GET", path+"/workspace/"+intentID+"/note.txt?download=1", nil, adminCookies, ""); out.Code != 200 || out.Body.String() != "verified note" {
		t.Fatalf("download: %d %s", out.Code, out.Body.String())
	}
	if out := call("GET", path+"/workspace", nil, viewerCookies, ""); out.Code != 403 {
		t.Fatalf("viewer files: %d", out.Code)
	}
	if out := call("GET", "/api/v1/audit-log", nil, adminCookies, ""); out.Code != 200 || !strings.Contains(out.Body.String(), "conversation.steer") {
		t.Fatalf("audit: %d %s", out.Code, out.Body.String())
	}
	if out := call("GET", "/api/v1/audit-log", nil, viewerCookies, ""); out.Code != 403 {
		t.Fatalf("viewer audit: %d", out.Code)
	}
	if out := call("GET", path+"/conversation", nil, viewerCookies, ""); out.Code != 200 || !strings.Contains(out.Body.String(), "先验证首页") {
		t.Fatalf("viewer conversation: %d %s", out.Code, out.Body.String())
	}
	if out := call("GET", "/api/v1/dashboard", nil, viewerCookies, ""); out.Code != 200 || !strings.Contains(out.Body.String(), "active_workers") {
		t.Fatalf("dashboard: %d %s", out.Code, out.Body.String())
	}
}
