package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/X9st/TianFaSRC/internal/agent"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/chromedp/chromedp"
	"github.com/jackc/pgx/v5/pgxpool"
)

func runPhase7RateLimitBrowserSequence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scheduler agent.Scheduler, profiles llm.Store, orgID, actorID, targetURL string, targetPort int, targetHits *atomic.Int32, browserCtx context.Context, browserURL string) {
	t.Helper()
	var primaryCalls, backupCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		w.Header().Set("Retry-After", "1")
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer backup-429-secret" {
			t.Error("backup Profile key was not isolated")
			http.Error(w, "bad credential", http.StatusUnauthorized)
			return
		}
		var response string
		if backupCalls.Add(1) == 1 {
			response = fmt.Sprintf(`{"tool_calls":[{"name":"http.read","input":{"url":%q,"method":"GET"}}]}`, targetURL)
		} else {
			response = `{"summary":"备用 Profile 完成范围内 HTTP 观察"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": response}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10}})
	}))
	defer backup.Close()
	enabled, stream := true, false
	create := func(name, endpoint, credential string) llm.Profile {
		profile, err := profiles.Create(ctx, orgID, actorID, llm.CreateInput{Name: name, Format: "openai-chat", BaseURL: endpoint + "/v1", Model: "phase7-same-model", Credential: llm.CredentialWrite{SecretValue: credential}, Enabled: &enabled, Stream: &stream, ContextWindow: 8192, MaxOutputTokens: 512, RatePerSecond: 100, RatePerMinute: 1000})
		if err != nil {
			t.Fatal(err)
		}
		return profile
	}
	first := create("primary-429", primary.URL, "primary-429-secret")
	second := create("backup-429", backup.URL, "backup-429-secret")
	if err := profiles.SetDefaultChain(ctx, orgID, actorID, []string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	item, err := (task.Store{Pool: pool}).Create(ctx, orgID, actorID, task.CreateInput{Title: "Phase 7 限流切换验收", Goals: []task.GoalInput{{Text: "主模型限流后备用模型完成观察", Priority: 5}}, Scope: struct {
		Entries []task.ScopeEntryInput `json:"entries"`
	}{Entries: []task.ScopeEntryInput{{Effect: "allow", Kind: "ip", Value: "127.0.0.1", Ports: []int{targetPort}}}}})
	if err != nil {
		t.Fatal(err)
	}
	taskID := item.Task.ID
	intentID := identity.NewID()
	if _, err := pool.Exec(ctx, `UPDATE tasks SET status='running' WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'intent','open','限流时保留单个 Worker Run')`, intentID, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key,budget_model_rounds,budget_tool_calls) VALUES($1,$2,$3,'限流时保留单个 Worker Run','单次 HTTP 观察',$4,5,3)`, intentID, taskID, item.Goals[0].ID, "phase7-rate-limit-browser"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind) VALUES($1,$2,$3,'spawns')`, taskID, item.Goals[0].ID, intentID); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := scheduler.ClaimWorker(ctx)
	if err != nil || !ok || claim.IntentID != intentID {
		t.Fatalf("429 Worker claim: %+v %v %v", claim, ok, err)
	}
	hitsBefore := targetHits.Load()
	if err := scheduler.RunWorker(ctx, claim); err != nil {
		t.Fatal(err)
	}
	var status, health, circuit string
	var cooldown *time.Time
	if err := pool.QueryRow(ctx, `SELECT status FROM task_intents WHERE id=$1`, intentID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT health_state,circuit_state,cooldown_until FROM llm_profiles WHERE id=$1`, first.ID).Scan(&health, &circuit, &cooldown); err != nil {
		t.Fatal(err)
	}
	var failovers, primaryAttempts, backupUsage, httpCalls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE task_id=$1 AND event_type='llm.failover' AND payload->'data'->>'error_class'='rate_limit'`, taskID).Scan(&failovers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM llm_attempts WHERE run_id=$1 AND profile_id=$2 AND status='failed' AND error_class='rate_limit'`, claim.RunID, first.ID).Scan(&primaryAttempts); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM llm_attempts WHERE run_id=$1 AND profile_id=$2 AND status='succeeded' AND input_tokens=20 AND output_tokens=10 AND estimated=false`, claim.RunID, second.ID).Scan(&backupUsage); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tool_calls WHERE run_id=$1 AND name='http.read' AND status='succeeded'`, claim.RunID).Scan(&httpCalls); err != nil {
		t.Fatal(err)
	}
	if status != "done" || primaryCalls.Load() != 1 || backupCalls.Load() != 2 || targetHits.Load() != hitsBefore+1 || failovers != 1 || primaryAttempts != 1 || backupUsage != 2 || httpCalls != 1 || health != "degraded" || circuit != "closed" || cooldown == nil || !cooldown.After(time.Now()) {
		t.Fatalf("429 recovery: status=%s primary=%d backup=%d hits=%d failovers=%d attempts=%d usage=%d http=%d health=%s circuit=%s cooldown=%v", status, primaryCalls.Load(), backupCalls.Load(), targetHits.Load()-hitsBefore, failovers, primaryAttempts, backupUsage, httpCalls, health, circuit, cooldown)
	}
	if _, err := (task.Store{Pool: pool}).ProjectActivities(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Do(browserCtx, chromedp.Navigate(browserURL+"/tasks/?id="+taskID), chromedp.WaitVisible(chromedp.CSS(".task-summary"))); err != nil {
		t.Fatal(err)
	}
	for _, visible := range []string{"Phase 7 限流切换验收", "llm.failover", "http.read"} {
		if err := browserWaitText(browserCtx, visible); err != nil {
			t.Fatal(err)
		}
	}
	if err := browserClickButton(browserCtx, "查看记录"); err != nil {
		t.Fatal(err)
	}
	for _, visible := range []string{"模型尝试", "请求限流", "0/0 Token", "20/10 Token"} {
		if err := browserWaitText(browserCtx, visible); err != nil {
			t.Fatal(err)
		}
	}
}
