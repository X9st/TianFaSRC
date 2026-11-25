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
	"strings"
	"testing"

	"github.com/X9st/TianFaSRC/internal/auth"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/secret"
	"github.com/X9st/TianFaSRC/internal/store/postgres"
	"github.com/X9st/TianFaSRC/internal/system"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase2ProfileAPI(t *testing.T) {
	base := os.Getenv("TIANFA_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TIANFA_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	config, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := "tianfa_p2api_" + strings.ReplaceAll(identity.NewID(), "-", "")[16:]
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
	user, err := (auth.Store{Pool: pool}).InitAdmin(ctx, "admin@example.invalid", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	_, err = (auth.Store{Pool: pool}).CreateUser(ctx, user.OrganizationID, user.ID, "operator@example.invalid", "another correct horse battery", "operator")
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := secret.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test", Pool: pool, Secrets: &secrets, Readiness: system.Readiness{Database: pool, DataDir: t.TempDir()}})
	call := func(method, path string, body any, cookies []*http.Cookie, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		var input io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, input)
		req.Host = "localhost"
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	login := func(email, password string) ([]*http.Cookie, string) {
		rec := call("POST", "/api/v1/auth/login", map[string]any{"email": email, "password": password}, nil, "")
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
		cookies := rec.Result().Cookies()
		for _, cookie := range cookies {
			if cookie.Name == csrfCookie {
				return cookies, cookie.Value
			}
		}
		t.Fatal("missing CSRF")
		return nil, ""
	}
	cookies, csrf := login("admin@example.invalid", "correct horse battery staple")
	payload := map[string]any{"name": "primary", "format": "openai-chat", "base_url": "https://api.example.invalid/v1", "model": "stub", "credential": map[string]any{"secret_value": "very-private-secret"}, "context_window": 4096, "max_output_tokens": 512, "rate_per_second": 2, "rate_per_minute": 60}
	if rec := call("POST", "/api/v1/llm-profiles", payload, cookies, ""); rec.Code != 403 {
		t.Fatalf("CSRF status %d", rec.Code)
	}
	created := call("POST", "/api/v1/llm-profiles", payload, cookies, csrf)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "very-private-secret") {
		t.Fatal("secret echoed")
	}
	var response struct {
		Data struct {
			ID             string `json:"id"`
			CredentialRef  string `json:"credential_ref"`
			CredentialHint string `json:"credential_hint"`
		}
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id := response.Data.ID
	if id == "" || response.Data.CredentialRef == "" || response.Data.CredentialHint != "cret" {
		t.Fatalf("response %+v", response.Data)
	}
	listed := call("GET", "/api/v1/llm-profiles", nil, cookies, "")
	if listed.Code != 200 || strings.Contains(listed.Body.String(), "very-private-secret") {
		t.Fatalf("list leaked or failed: %s", listed.Body.String())
	}
	hostChange := call("PATCH", "/api/v1/llm-profiles/"+id, map[string]any{"base_url": "https://different.example.invalid/v1"}, cookies, csrf)
	if hostChange.Code != 422 {
		t.Fatalf("host change without credential: %d %s", hostChange.Code, hostChange.Body.String())
	}
	if rec := call("PUT", "/api/v1/llm-default-chain", map[string]any{"profiles": []string{id}}, cookies, csrf); rec.Code != 200 {
		t.Fatalf("chain: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("DELETE", "/api/v1/llm-profiles/"+id, nil, cookies, csrf); rec.Code != 409 {
		t.Fatalf("referenced profile delete: %d %s", rec.Code, rec.Body.String())
	}
	operatorCookies, operatorCSRF := login("operator@example.invalid", "another correct horse battery")
	if rec := call("POST", "/api/v1/llm-profiles", payload, operatorCookies, operatorCSRF); rec.Code != 403 {
		t.Fatalf("operator create: %d", rec.Code)
	}
	if rec := call("GET", "/api/v1/llm-profiles", nil, operatorCookies, ""); rec.Code != 200 {
		t.Fatalf("operator read: %d", rec.Code)
	}
}
