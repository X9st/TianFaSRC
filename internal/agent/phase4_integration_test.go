package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/X9st/TianFaSRC/internal/mcpclient"
	"github.com/X9st/TianFaSRC/internal/policy"
	"github.com/X9st/TianFaSRC/internal/secret"
	"github.com/X9st/TianFaSRC/internal/skill"
	"github.com/X9st/TianFaSRC/internal/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolCallIdempotencyScopeAndAudit(t *testing.T) {
	s, orgID, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("verified"))
	}))
	defer server.Close()
	port, _ := strconv.Atoi(strings.Split(server.URL, ":")[2])
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE task_scopes SET kind='ip',value='127.0.0.1',normalized_value='127.0.0.1',ports=ARRAY[$2]::int[] WHERE task_id=$1`, taskID, port); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE task_intents SET status='running' WHERE id=$1`, c.IntentID); err != nil {
		t.Fatal(err)
	}
	d := tool.Dispatcher{Pool: s.Engine.Pool, DataDir: t.TempDir(), Enforcer: policy.Enforcer{TestOnlyAllowPrivate: true}}
	input, _ := json.Marshal(map[string]any{"url": server.URL, "method": "GET"})
	call := tool.Call{OrganizationID: orgID, TaskID: taskID, IntentID: c.IntentID, RunID: c.RunID, ActorID: c.ActorID, Name: "http.read", Input: input, IdempotencyKey: "test-http-idempotent"}
	first, err := d.Execute(ctx, call)
	if err != nil || first.Status != "succeeded" || first.ID == "" || hits.Load() != 1 {
		t.Fatalf("first=%+v hits=%d err=%v", first, hits.Load(), err)
	}
	second, err := d.Execute(ctx, call)
	if err != nil || first.ID != second.ID || hits.Load() != 1 {
		t.Fatalf("repeat=%+v hits=%d err=%v", second, hits.Load(), err)
	}
	call.IdempotencyKey = "test-http-outside-scope"
	call.Input, _ = json.Marshal(map[string]any{"url": "http://127.0.0.2:" + strconv.Itoa(port) + "/"})
	blocked, err := d.Execute(ctx, call)
	if !errors.Is(err, tool.ErrToolBlocked) || blocked.Status != "blocked" || hits.Load() != 1 {
		t.Fatalf("blocked=%+v hits=%d err=%v", blocked, hits.Load(), err)
	}
	var auditCount int
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='tool.policy_blocked' AND resource_id=$1`, blocked.ID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("policy audit=%d err=%v", auditCount, err)
	}
	if err := d.SetBinding(ctx, orgID, c.ActorID, "http.read", false); err != nil {
		t.Fatal(err)
	}
	visible, err := d.AvailableForRun(ctx, orgID, c.RunID)
	if err != nil || strings.Contains(strings.Join(visible, ","), "http.read") {
		t.Fatalf("disabled tool remains visible: %v %v", visible, err)
	}
	call.IdempotencyKey = "test-http-disabled-binding"
	call.Input = input
	blocked, err = d.Execute(ctx, call)
	if !errors.Is(err, tool.ErrToolBlocked) || blocked.Status != "blocked" || hits.Load() != 1 {
		t.Fatalf("disabled binding: %+v hits=%d err=%v", blocked, hits.Load(), err)
	}
}

func TestMCPDeferredUntilSkillLoad(t *testing.T) {
	s, orgID, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1.0"}, nil)
	var hits atomic.Int32
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "return text"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		hits.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()
	claim, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE task_intents SET status='running' WHERE id=$1`, claim.IntentID); err != nil {
		t.Fatal(err)
	}
	enforcer := policy.Enforcer{TestOnlyAllowPrivate: true}
	store := mcpclient.Store{Pool: s.Engine.Pool, Enforcer: enforcer, TestOnlyAllowHTTPURL: true}
	if _, err := store.Create(ctx, orgID, claim.ActorID, mcpclient.Server{Name: "fixture", Transport: "streamable_http", Endpoint: httpServer.URL}); err != nil {
		t.Fatal(err)
	}
	tools, err := store.Discover(ctx, orgID, "fixture")
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("discover: %+v %v", tools, err)
	}
	if err := store.SetEnabled(ctx, orgID, claim.ActorID, "fixture", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetToolEnabled(ctx, orgID, claim.ActorID, "fixture", "echo", true); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	file, err := zipWriter.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("---\nname: fixture\nversion: 1.0.0\ndescription: MCP fixture\nagents: [worker]\nmcps: [fixture]\n---\nUse the echo tool."))
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (skill.Store{Pool: s.Engine.Pool}).InstallZIP(ctx, orgID, claim.ActorID, archive.Bytes()); err != nil {
		t.Fatal(err)
	}
	dispatcher := tool.Dispatcher{Pool: s.Engine.Pool, DataDir: t.TempDir(), Enforcer: enforcer}
	before, err := dispatcher.AvailableForRun(ctx, orgID, claim.RunID)
	if err != nil || strings.Contains(strings.Join(before, ","), "mcp__fixture__echo") {
		t.Fatalf("MCP visible before skill load: %v %v", before, err)
	}
	call := tool.Call{OrganizationID: orgID, TaskID: taskID, IntentID: claim.IntentID, RunID: claim.RunID, ActorID: claim.ActorID, Name: "skill.load", Input: json.RawMessage(`{"name":"fixture"}`), IdempotencyKey: "fixture-skill-load"}
	loaded, err := dispatcher.Execute(ctx, call)
	if err != nil || loaded.Status != "succeeded" {
		t.Fatalf("load: %+v %v", loaded, err)
	}
	after, err := dispatcher.AvailableForRun(ctx, orgID, claim.RunID)
	if err != nil || !strings.Contains(strings.Join(after, ","), "mcp__fixture__echo") {
		t.Fatalf("MCP unavailable after skill load: %v %v", after, err)
	}
	call.Name = "mcp__fixture__echo"
	call.Input = json.RawMessage(`{"text":"verified"}`)
	call.IdempotencyKey = "fixture-mcp-echo"
	result, err := dispatcher.Execute(ctx, call)
	if err != nil || result.Status != "succeeded" || hits.Load() != 1 {
		t.Fatalf("MCP result: %+v hits=%d err=%v", result, hits.Load(), err)
	}
	call.Input = json.RawMessage(`{"text":"verified","unexpected":true}`)
	call.IdempotencyKey = "fixture-mcp-invalid"
	blocked, err := dispatcher.Execute(ctx, call)
	if !errors.Is(err, tool.ErrToolBlocked) || blocked.Status != "blocked" || hits.Load() != 1 {
		t.Fatalf("MCP schema block: %+v hits=%d err=%v", blocked, hits.Load(), err)
	}
	call.Input = json.RawMessage(`{"text":"http://127.0.0.2:9999/private"}`)
	call.IdempotencyKey = "fixture-mcp-outside"
	blocked, err = dispatcher.Execute(ctx, call)
	if !errors.Is(err, tool.ErrToolBlocked) || blocked.Status != "blocked" || hits.Load() != 1 {
		t.Fatalf("MCP scope block: %+v hits=%d err=%v", blocked, hits.Load(), err)
	}
}

func TestToolApprovalResumesWithSameCallID(t *testing.T) {
	s, orgID, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE tasks SET policy_mode='approval' WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE task_intents SET status='running' WHERE id=$1`, c.IntentID); err != nil {
		t.Fatal(err)
	}
	d := tool.Dispatcher{Pool: s.Engine.Pool, DataDir: t.TempDir()}
	input, _ := json.Marshal(map[string]any{"name": "note.txt", "content": "approved"})
	call := tool.Call{OrganizationID: orgID, TaskID: taskID, IntentID: c.IntentID, RunID: c.RunID, ActorID: c.ActorID, Name: "workspace.write", Input: input, IdempotencyKey: "test-approved-write"}
	pending, err := d.Execute(ctx, call)
	if !errors.Is(err, tool.ErrApprovalRequired) || pending.Status != "policy_check" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := s.FinishWorker(ctx, c, "blocked", "等待审批", nil); err != nil {
		t.Fatal(err)
	}
	approvals, err := d.ListApprovals(ctx, orgID)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%+v err=%v", approvals, err)
	}
	if err := d.DecideApproval(ctx, orgID, c.ActorID, approvals[0].ID, true, "授权工作区写入"); err != nil {
		t.Fatal(err)
	}
	next, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok || next.Attempt != 2 {
		t.Fatalf("retry claim=%+v ok=%v err=%v", next, ok, err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE task_intents SET status='running' WHERE id=$1`, next.IntentID); err != nil {
		t.Fatal(err)
	}
	call.RunID = next.RunID
	result, err := d.Execute(ctx, call)
	if err != nil || result.ID != pending.ID || result.Status != "succeeded" {
		t.Fatalf("approved result=%+v err=%v", result, err)
	}
	var runID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT run_id FROM tool_calls WHERE id=$1`, result.ID).Scan(&runID); err != nil || runID != next.RunID {
		t.Fatalf("execution run=%s err=%v", runID, err)
	}
	files, err := (tool.Workspace{Root: d.DataDir}).Execute(ctx, taskID, next.IntentID, "workspace.read", json.RawMessage(`{"name":"note.txt"}`))
	if err != nil || string(files.Output) != "approved" {
		t.Fatalf("workspace file=%q err=%v", files.Output, err)
	}
}

func TestWorkerModelToolLoopCreatesConfirmedFact(t *testing.T) {
	s, orgID, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte("authorized target"))
	}))
	defer target.Close()
	port, _ := strconv.Atoi(strings.Split(target.URL, ":")[2])
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE task_scopes SET kind='ip',value='127.0.0.1',normalized_value='127.0.0.1',ports=ARRAY[$2]::int[] WHERE task_id=$1`, taskID, port); err != nil {
		t.Fatal(err)
	}
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		text := fmt.Sprintf(`{"tool_calls":[{"name":"http.read","input":{"url":%q,"method":"GET"}}]}`, target.URL)
		if modelCalls.Add(1) == 2 {
			if !strings.Contains(payload.Messages[len(payload.Messages)-1].Content, "authorized target") {
				t.Error("model did not receive tool output")
			}
			text = `{"summary":"授权站点已响应"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10}})
	}))
	defer model.Close()
	secured, err := secret.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.Engine.Profiles = llm.Store{Pool: s.Engine.Pool, Secrets: secured}
	s.Engine.Router = llm.Router{Store: s.Engine.Profiles}
	var actorID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT created_by FROM tasks WHERE id=$1`, taskID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	enabled, stream := true, false
	profile, err := s.Engine.Profiles.Create(ctx, orgID, actorID, llm.CreateInput{Name: "test worker", Format: "openai-chat", BaseURL: model.URL + "/v1", Model: "fake", Credential: llm.CredentialWrite{SecretValue: "local-test"}, Enabled: &enabled, Stream: &stream, ContextWindow: 8192, MaxOutputTokens: 512, RatePerSecond: 100, RatePerMinute: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Profiles.SetDefaultChain(ctx, orgID, actorID, []string{profile.ID}); err != nil {
		t.Fatal(err)
	}
	dispatcher := &tool.Dispatcher{Pool: s.Engine.Pool, DataDir: t.TempDir(), Enforcer: policy.Enforcer{TestOnlyAllowPrivate: true}}
	s.Tools = dispatcher
	claim, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	if err := s.RunWorker(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if targetHits.Load() != 1 || modelCalls.Load() != 2 {
		t.Fatalf("target=%d model=%d", targetHits.Load(), modelCalls.Load())
	}
	var intentStatus string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT status FROM task_intents WHERE id=$1`, intentID).Scan(&intentStatus); err != nil || intentStatus != "done" {
		t.Fatalf("intent=%s err=%v", intentStatus, err)
	}
	var facts, calls, attempts int
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM exploration_nodes WHERE task_id=$1 AND source_intent_id=$2 AND kind='fact' AND state='confirmed' AND tool_call_id IS NOT NULL`, taskID, intentID).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM tool_calls WHERE task_id=$1 AND status='succeeded'`, taskID).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM llm_attempts WHERE run_id=$1`, claim.RunID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if facts != 1 || calls != 1 || attempts != 2 {
		t.Fatalf("facts=%d calls=%d attempts=%d", facts, calls, attempts)
	}
}
