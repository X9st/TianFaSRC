package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/X9st/TianFaSRC/internal/auth"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/X9st/TianFaSRC/internal/secret"
	"github.com/X9st/TianFaSRC/internal/store/postgres"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeObservation struct{}

func (fakeObservation) Observe(_ context.Context, _, _ string) (Observation, error) {
	return Observation{Summary: "受控测试工具观察到 HTTP 200", ToolCallID: identity.NewID(), Confirmed: true}, nil
}

func TestPlanIntentFactAndQuotaFailover(t *testing.T) {
	base := os.Getenv("TIANFA_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TIANFA_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	adminConfig, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	dbName := "tianfa_agent_" + strings.ReplaceAll(identity.NewID(), "-", "")[16:]
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+dbName) })
	poolConfig, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = dbName
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
	secrets, err := secret.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profiles := llm.Store{Pool: pool, Secrets: secrets}
	var primaryCalls, backupCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(402)
		fmt.Fprint(w, `{"error":"quota exhausted"}`)
	}))
	defer primary.Close()
	var goalID string
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer backup-secret" {
			t.Errorf("wrong backup credential")
			w.WriteHeader(401)
			return
		}
		var payload struct {
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		text := `{"summary":"已处理受控观察"}`
		if len(payload.Messages) > 0 {
			system := payload.Messages[0].Content
			if strings.Contains(system, "directions") {
				text = fmt.Sprintf(`{"directions":[{"goal_id":"%s","objective":"验证 HTTP 状态"}]}`, goalID)
			} else if strings.Contains(system, "planning_blocked") {
				text = fmt.Sprintf(`{"intents":[{"goal_id":"%s","objective":"读取授权站点首页状态","success_criteria":"受控工具返回 HTTP 状态"}],"planning_blocked":""}`, goalID)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10}})
	}))
	defer backup.Close()
	makeProfile := func(name, url, key string) llm.Profile {
		enabled, stream := true, false
		p, err := profiles.Create(ctx, user.OrganizationID, user.ID, llm.CreateInput{Name: name, Format: "openai-chat", BaseURL: url + "/v1", Model: "stub", Credential: llm.CredentialWrite{SecretValue: key}, Priority: 100, Enabled: &enabled, Stream: &stream, ContextWindow: 4096, MaxOutputTokens: 512, RatePerSecond: 100, RatePerMinute: 1000})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := makeProfile("primary", primary.URL, "primary-secret")
	second := makeProfile("backup", backup.URL, "backup-secret")
	if err := profiles.SetDefaultChain(ctx, user.OrganizationID, user.ID, []string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT ciphertext FROM secret_values WHERE id=$1`, first.CredentialRef).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "primary-secret") {
		t.Fatal("credential stored in plaintext")
	}
	createTask := func() task.Detail {
		item, err := (task.Store{Pool: pool}).Create(ctx, user.OrganizationID, user.ID, task.CreateInput{Title: "授权验证", Description: "本地测试", Goals: []task.GoalInput{{Text: "验证可访问", Priority: 5}}, Scope: struct {
			Entries []task.ScopeEntryInput `json:"entries"`
		}{Entries: []task.ScopeEntryInput{{Effect: "allow", Kind: "domain", Value: "example.invalid"}}}})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	item := createTask()
	goalID = item.Goals[0].ID
	engine := Engine{Pool: pool, Profiles: profiles, Router: llm.Router{Store: profiles}, Observations: fakeObservation{}}
	if err := engine.Start(ctx, user.OrganizationID, user.ID, item.Task.ID, item.Task.Version); err != nil {
		t.Fatal(err)
	}
	intents, err := engine.ListIntents(ctx, user.OrganizationID, item.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].Status != "done" {
		t.Fatalf("intents: %+v", intents)
	}
	graph, err := (task.Store{Pool: pool}).Graph(ctx, user.OrganizationID, item.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	facts := 0
	var factID string
	for _, node := range graph.Nodes {
		if node.Kind == "fact" {
			facts++
			factID = node.ID
			if node.SourceIntentID == nil || node.RunID == nil || node.ToolCallID == nil {
				t.Fatalf("missing lineage: %+v", node)
			}
		}
	}
	if facts != 1 {
		t.Fatalf("facts=%d", facts)
	}
	if err := engine.ProveGoal(ctx, user.OrganizationID, item.Task.ID, item.Goals[0].ID, []string{item.Goals[0].ID}); err == nil {
		t.Fatal("goal node accepted as evidence")
	}
	if err := engine.ProveGoal(ctx, user.OrganizationID, item.Task.ID, item.Goals[0].ID, []string{factID}); err != nil {
		t.Fatal(err)
	}
	var goalState string
	if err := pool.QueryRow(ctx, `SELECT state FROM task_goals WHERE id=$1`, item.Goals[0].ID).Scan(&goalState); err != nil {
		t.Fatal(err)
	}
	if goalState != "met" {
		t.Fatalf("goal state=%s", goalState)
	}
	runs, err := engine.ListRuns(ctx, user.OrganizationID, item.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("runs=%d", len(runs))
	}
	workerDetail, err := engine.GetRun(ctx, user.OrganizationID, runs[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	untrusted := false
	for _, message := range workerDetail.Messages {
		if message.TrustLevel == "untrusted_observation" {
			untrusted = true
		}
		if message.Role == "system" && !strings.Contains(message.Content, "不可信观察") {
			t.Fatal("worker invariant missing")
		}
	}
	if !untrusted {
		t.Fatal("observation was not marked untrusted")
	}
	if primaryCalls.Load() != 1 || backupCalls.Load() != 3 {
		t.Fatalf("calls primary=%d backup=%d", primaryCalls.Load(), backupCalls.Load())
	}
	var failover, measured int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE task_id=$1 AND event_type='llm.failover'`, item.Task.ID).Scan(&failover); err != nil {
		t.Fatal(err)
	}
	if failover != 1 {
		t.Fatalf("failovers=%d", failover)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM llm_attempts WHERE profile_id=$1 AND input_tokens=20 AND output_tokens=10 AND estimated=false`, second.ID).Scan(&measured); err != nil {
		t.Fatal(err)
	}
	if measured != 3 {
		t.Fatalf("measured attempts=%d", measured)
	}
	if err := pool.QueryRow(ctx, `SELECT health_state FROM llm_profiles WHERE id=$1`, first.ID).Scan(&first.HealthState); err != nil {
		t.Fatal(err)
	}
	if first.HealthState != "open_circuit" {
		t.Fatalf("health=%s", first.HealthState)
	}
	updatedPrompt, err := profiles.UpdatePrompt(ctx, user.OrganizationID, user.ID, "planner", "根据目标输出 intents、proofs 和 planning_blocked。")
	if err != nil {
		t.Fatal(err)
	}
	if updatedPrompt.Version != 2 || updatedPrompt.ContentHash == runs[1].PromptHash {
		t.Fatalf("prompt version/hash: %+v", updatedPrompt)
	}
	withoutTools := createTask()
	goalID = withoutTools.Goals[0].ID
	engine.Observations = nil
	if err := engine.Start(ctx, user.OrganizationID, user.ID, withoutTools.Task.ID, withoutTools.Task.Version); err != nil {
		t.Fatal(err)
	}
	blockedIntents, err := engine.ListIntents(ctx, user.OrganizationID, withoutTools.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blockedIntents) != 1 || blockedIntents[0].Status != "blocked" || blockedIntents[0].BlockingReason == nil {
		t.Fatalf("unverified intent: %+v", blockedIntents)
	}
	newRuns, err := engine.ListRuns(ctx, user.OrganizationID, withoutTools.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(newRuns) != 2 || newRuns[1].PromptVersion != 2 || newRuns[1].PromptHash != updatedPrompt.ContentHash {
		t.Fatalf("new prompt run: %+v", newRuns)
	}
	blockedGraph, err := (task.Store{Pool: pool}).Graph(ctx, user.OrganizationID, withoutTools.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range blockedGraph.Nodes {
		if node.Kind == "fact" {
			t.Fatal("model-only run fabricated a fact")
		}
	}
	engine.Observations = fakeObservation{}
	if err := engine.Advance(ctx, user.OrganizationID, user.ID, withoutTools.Task.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := engine.ListIntents(ctx, user.OrganizationID, withoutTools.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || resumed[0].Status != "done" {
		t.Fatalf("resumed intent: %+v", resumed)
	}
	resumedGraph, err := (task.Store{Pool: pool}).Graph(ctx, user.OrganizationID, withoutTools.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	countFacts := 0
	for _, node := range resumedGraph.Nodes {
		if node.Kind == "fact" {
			countFacts++
		}
	}
	if countFacts != 1 {
		t.Fatalf("resumed facts=%d", countFacts)
	}
}
