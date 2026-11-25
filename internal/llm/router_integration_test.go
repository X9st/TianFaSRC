package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/X9st/TianFaSRC/internal/auth"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/secret"
	"github.com/X9st/TianFaSRC/internal/store/postgres"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func routerTestStore(t *testing.T) (context.Context, *pgxpool.Pool, Store, string, string) {
	t.Helper()
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
	name := "tianfa_llm_" + strings.ReplaceAll(identity.NewID(), "-", "")[16:]
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+name); admin.Close(ctx) })
	poolConfig, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	user, err := (auth.Store{Pool: pool}).InitAdmin(ctx, "admin@example.invalid", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := secret.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ctx, pool, Store{Pool: pool, Secrets: secrets}, user.OrganizationID, user.ID
}
func createRouterProfile(t *testing.T, ctx context.Context, s Store, orgID, actorID, name, base string, stream bool, rate int) Profile {
	t.Helper()
	enabled := true
	p, err := s.Create(ctx, orgID, actorID, CreateInput{Name: name, Format: "openai-chat", BaseURL: base + "/v1", Model: "stub", Credential: CredentialWrite{SecretValue: name + "-secret"}, Enabled: &enabled, Stream: &stream, ContextWindow: 4096, MaxOutputTokens: 256, RatePerSecond: rate, RatePerMinute: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSharedLimiterAndHalfOpenProbe(t *testing.T) {
	ctx, pool, store, orgID, actorID := routerTestStore(t)
	p := createRouterProfile(t, ctx, store, orgID, actorID, "limited", "http://127.0.0.1:17999", false, 2)
	router := Router{Store: store}
	var allowed int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := router.reserve(ctx, p)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 2 {
		t.Fatalf("allowed=%d want 2", allowed)
	}
	var used int
	if err := pool.QueryRow(ctx, `SELECT second_used FROM llm_profiles WHERE id=$1`, p.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 2 {
		t.Fatalf("used=%d", used)
	}
	failure := &ProviderError{Class: "rate_limit", Status: 429}
	for i := 0; i < 3; i++ {
		if err := router.recordHealth(ctx, p.ID, failure); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.Get(ctx, orgID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.HealthState != "open_circuit" {
		t.Fatalf("state=%s", current.HealthState)
	}
	if current.CircuitState != "open" {
		t.Fatalf("circuit=%s", current.CircuitState)
	}
	ok, until, err := router.reserve(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if ok || until == nil {
		t.Fatal("open circuit allowed early probe")
	}
	if _, err := pool.Exec(ctx, `UPDATE llm_profiles SET cooldown_until=now()-interval '1 second',second_window_at=now()-interval '2 seconds',minute_window_at=now()-interval '2 minutes' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	allowed = 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _, err := router.reserve(ctx, p)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("half-open probes=%d want 1", allowed)
	}
	if err := router.recordHealth(ctx, p.ID, nil); err != nil {
		t.Fatal(err)
	}
	current, err = store.Get(ctx, orgID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.HealthState != "healthy" {
		t.Fatalf("recovered state=%s", current.HealthState)
	}
	if current.CircuitState != "closed" {
		t.Fatalf("recovered circuit=%s", current.CircuitState)
	}
}

func TestPartialStreamContinuesOnBackup(t *testing.T) {
	ctx, pool, store, orgID, actorID := routerTestStore(t)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"前半句\"}}]}\n\n")
	}))
	defer primary.Close()
	sawContinuation := false
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		for _, m := range request.Messages {
			if m.Role == "assistant" && m.Content == "前半句" {
				sawContinuation = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"后半句"}}]}`)
	}))
	defer backup.Close()
	first := createRouterProfile(t, ctx, store, orgID, actorID, "streamer", primary.URL, true, 50)
	second := createRouterProfile(t, ctx, store, orgID, actorID, "backup", backup.URL, false, 50)
	if err := store.SetDefaultChain(ctx, orgID, actorID, []string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	item, err := (task.Store{Pool: pool}).Create(ctx, orgID, actorID, task.CreateInput{Title: "stream test", Goals: []task.GoalInput{{Text: "验证", Priority: 5}}, Scope: struct {
		Entries []task.ScopeEntryInput `json:"entries"`
	}{Entries: []task.ScopeEntryInput{{Effect: "allow", Kind: "domain", Value: "example.invalid"}}}})
	if err != nil {
		t.Fatal(err)
	}
	runID := identity.NewID()
	if _, err := pool.Exec(ctx, `INSERT INTO agent_runs(id,organization_id,task_id,role,status,prompt_version,prompt_hash,session_key) VALUES($1,$2,$3,'planner','running',1,'hash','test')`, runID, orgID, item.Task.ID); err != nil {
		t.Fatal(err)
	}
	result, err := (Router{Store: store}).Complete(ctx, orgID, item.Task.ID, runID, "planner", []Message{{Role: "system", Content: "policy"}, {Role: "user", Content: "plan"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "前半句后半句" || !sawContinuation {
		t.Fatalf("result=%q continuation=%v", result.Text, sawContinuation)
	}
	var delivered string
	var status string
	if err := pool.QueryRow(ctx, `SELECT delivered_text,status FROM llm_attempts WHERE run_id=$1 AND profile_id=$2`, runID, first.ID).Scan(&delivered, &status); err != nil {
		t.Fatal(err)
	}
	if delivered != "前半句" || status != "partial" {
		t.Fatalf("first attempt %q %s", delivered, status)
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE task_id=$1 AND event_type='llm.failover'`, item.Task.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("failovers=%d", events)
	}
	if _, err := pool.Exec(ctx, `UPDATE llm_profiles SET enabled=false WHERE id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	anotherRun := identity.NewID()
	if _, err := pool.Exec(ctx, `INSERT INTO agent_runs(id,organization_id,task_id,role,status,prompt_version,prompt_hash,session_key) VALUES($1,$2,$3,'planner','running',1,'hash','test2')`, anotherRun, orgID, item.Task.ID); err != nil {
		t.Fatal(err)
	}
	_, err = (Router{Store: store}).Complete(ctx, orgID, item.Task.ID, anotherRun, "planner", []Message{{Role: "user", Content: "plan"}})
	var blocked *BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("expected recoverable blocked: %v", err)
	}
	if blocked.Until == nil || blocked.Until.Before(time.Now()) {
		t.Fatalf("cooldown=%v", blocked.Until)
	}
}

func TestAgentSubchainStaysWithinTaskChain(t *testing.T) {
	ctx, pool, store, orgID, actorID := routerTestStore(t)
	first := createRouterProfile(t, ctx, store, orgID, actorID, "first", "http://127.0.0.1:17999", false, 20)
	second := createRouterProfile(t, ctx, store, orgID, actorID, "second", "http://127.0.0.1:17998", false, 20)
	if err := store.SetDefaultChain(ctx, orgID, actorID, []string{first.ID}); err != nil {
		t.Fatal(err)
	}
	item, err := (task.Store{Pool: pool}).Create(ctx, orgID, actorID, task.CreateInput{Title: "route test", Goals: []task.GoalInput{{Text: "验证", Priority: 5}}, Scope: struct {
		Entries []task.ScopeEntryInput `json:"entries"`
	}{Entries: []task.ScopeEntryInput{{Effect: "allow", Kind: "domain", Value: "example.invalid"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskChain(ctx, orgID, actorID, item.Task.ID, "worker", []string{second.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out-of-chain route accepted: %v", err)
	}
	if err := store.SetTaskChain(ctx, orgID, actorID, item.Task.ID, "", []string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTaskChain(ctx, orgID, actorID, item.Task.ID, "worker", []string{second.ID}); err != nil {
		t.Fatal(err)
	}
	route, err := store.ResolvedChain(ctx, orgID, item.Task.ID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].ID != second.ID {
		t.Fatalf("route=%+v", route)
	}
	if err := store.SetTaskChain(ctx, orgID, actorID, item.Task.ID, "", []string{first.ID}); err != nil {
		t.Fatal(err)
	}
	route, err = store.ResolvedChain(ctx, orgID, item.Task.ID, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].ID != first.ID {
		t.Fatalf("shrunk route=%+v", route)
	}
}
