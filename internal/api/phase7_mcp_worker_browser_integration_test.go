package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/X9st/TianFaSRC/internal/agent"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/chromedp/chromedp"
	"github.com/jackc/pgx/v5/pgxpool"
)

func runPhase7MCPWorkerSequence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scheduler agent.Scheduler, profiles llm.Store, orgID, actorID, targetURL string, targetPort int, browserCtx context.Context, browserURL string) {
	t.Helper()
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("MCP Worker model request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var response string
		switch modelCalls.Add(1) {
		case 1:
			for _, message := range input.Messages {
				if strings.Contains(message.Content, "mcp__browser-fixture__navigate") {
					t.Error("MCP tool name or schema entered model context before Skill load")
				}
			}
			response = `{"tool_calls":[{"name":"skill.load","input":{"name":"browser-fixture"}}]}`
		case 2:
			foundSchema := false
			for _, message := range input.Messages {
				foundSchema = foundSchema || message.Role == "user" && strings.Contains(message.Content, "输入 JSON Schema") && strings.Contains(message.Content, "mcp__browser-fixture__navigate") && strings.Contains(message.Content, `"url"`)
			}
			if !foundSchema {
				t.Error("newly unlocked MCP input schema missing from untrusted model context")
			}
			response = fmt.Sprintf(`{"tool_calls":[{"name":"mcp__browser-fixture__navigate","input":{"url":%q,"note":"normal"}}]}`, targetURL)
		case 3:
			response = fmt.Sprintf(`{"tool_calls":[{"name":"http.read","input":{"url":%q,"method":"GET"}}]}`, targetURL)
		default:
			response = `{"summary":"已通过 Browser MCP 与 HTTP 工具读取受控页面"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": response}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 10}})
	}))
	defer model.Close()
	enabled, stream := true, false
	profile, err := profiles.Create(ctx, orgID, actorID, llm.CreateInput{Name: "mcp-worker-stub", Format: "openai-chat", BaseURL: model.URL + "/v1", Model: "mcp-worker-stub", Credential: llm.CredentialWrite{SecretValue: "mcp-worker-secret"}, Enabled: &enabled, Stream: &stream, ContextWindow: 8192, MaxOutputTokens: 512, RatePerSecond: 100, RatePerMinute: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err := profiles.SetDefaultChain(ctx, orgID, actorID, []string{profile.ID}); err != nil {
		t.Fatal(err)
	}
	item, err := (task.Store{Pool: pool}).Create(ctx, orgID, actorID, task.CreateInput{Title: "Browser MCP 模型工具链", Goals: []task.GoalInput{{Text: "用 Skill 和 MCP 验证受控页面", Priority: 5}}, Scope: struct {
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
	if _, err := pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'intent','open','使用 Browser MCP 的 Worker')`, intentID, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key,budget_model_rounds,budget_tool_calls) VALUES($1,$2,$3,'使用 Browser MCP 的 Worker','受控页面已读取',$4,6,6)`, intentID, taskID, item.Goals[0].ID, "phase7-mcp-worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind) VALUES($1,$2,$3,'spawns')`, taskID, item.Goals[0].ID, intentID); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := scheduler.ClaimWorker(ctx)
	if err != nil || !ok || claim.IntentID != intentID {
		t.Fatalf("MCP model Worker claim: %+v %v %v", claim, ok, err)
	}
	if err := scheduler.RunWorker(ctx, claim); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM task_intents WHERE id=$1`, intentID).Scan(&status); err != nil || status != "done" || modelCalls.Load() != 4 {
		t.Fatalf("MCP Worker model rounds=%d status=%s %v", modelCalls.Load(), status, err)
	}
	var loaded, mcpCalls, observed, schemas int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE name='skill.load' AND status='succeeded'),count(*) FILTER (WHERE name='mcp__browser-fixture__navigate' AND status='succeeded'),count(*) FILTER (WHERE name='http.read' AND status='succeeded') FROM tool_calls WHERE run_id=$1`, claim.RunID).Scan(&loaded, &mcpCalls, &observed); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_messages WHERE run_id=$1 AND role='user' AND trust_level='untrusted_observation' AND content LIKE '%输入 JSON Schema%' AND content LIKE '%mcp__browser-fixture__navigate%'`, claim.RunID).Scan(&schemas); err != nil || loaded != 1 || mcpCalls != 1 || observed != 1 || schemas != 1 {
		t.Fatalf("MCP Worker tools: load=%d mcp=%d http=%d schemas=%d %v", loaded, mcpCalls, observed, schemas, err)
	}
	if err := chromedp.Do(browserCtx, chromedp.Navigate(browserURL+"/tasks/?id="+taskID), chromedp.WaitVisible(chromedp.CSS(".task-summary"))); err != nil {
		t.Fatal(err)
	}
	for _, visible := range []string{"Browser MCP 模型工具链", "skill.load", "mcp__browser-fixture__navigate", "http.read"} {
		if err := browserWaitText(browserCtx, visible); err != nil {
			t.Fatal(err)
		}
	}
}
