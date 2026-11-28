package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/X9st/TianFaSRC/internal/agent"
	"github.com/X9st/TianFaSRC/internal/auth"
	"github.com/X9st/TianFaSRC/internal/blob"
	"github.com/X9st/TianFaSRC/internal/finding"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/X9st/TianFaSRC/internal/policy"
	"github.com/X9st/TianFaSRC/internal/secret"
	"github.com/X9st/TianFaSRC/internal/store/postgres"
	"github.com/X9st/TianFaSRC/internal/system"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/tool"
	"github.com/X9st/TianFaSRC/internal/traffic"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase7AutonomousHTTPFindingAndFailoverE2E(t *testing.T) {
	base := os.Getenv("TIANFA_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set TIANFA_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	dbName := "tianfa_e2e_" + strings.ReplaceAll(identity.NewID(), "-", "")[16:]
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+dbName); err != nil {
			t.Errorf("drop E2E database: %v", err)
		}
	}()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	operator, err := (auth.Store{Pool: pool}).InitAdmin(ctx, "phase7@example.invalid", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	secrets, err := secret.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	profiles := llm.Store{Pool: pool, Secrets: secrets}
	var targetHits, primaryCalls, backupCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		_, _ = io.WriteString(w, "controlled private-data fixture")
	}))
	defer target.Close()
	parsedTarget, _ := url.Parse(target.URL)
	port, _ := strconv.Atoi(parsedTarget.Port())
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":"quota exhausted"}`)
	}))
	defer primary.Close()
	var goalIDs []string
	var assetID string
	proofs := map[string]string{}
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer backup-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var payload struct {
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Messages) < 2 {
			t.Errorf("invalid model request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		system := payload.Messages[0].Content
		var response string
		switch {
		case strings.Contains(system, `"directions"`):
			response = fmt.Sprintf(`{"directions":[{"goal_id":%q,"objective":"检查方向 A"},{"goal_id":%q,"objective":"检查方向 B"}]}`, goalIDs[0], goalIDs[1])
		case strings.Contains(system, "planning_blocked"):
			if len(proofs) == 0 {
				response = fmt.Sprintf(`{"intents":[{"goal_id":%q,"objective":"检查方向 A","success_criteria":"本地 HTTP 观察"},{"goal_id":%q,"objective":"检查方向 B","success_criteria":"本地 HTTP 观察"}],"planning_blocked":""}`, goalIDs[0], goalIDs[1])
			} else {
				response = fmt.Sprintf(`{"proofs":[{"goal_id":%q,"evidence_node_ids":[%q]},{"goal_id":%q,"evidence_node_ids":[%q]}]}`, goalIDs[0], proofs[goalIDs[0]], goalIDs[1], proofs[goalIDs[1]])
			}
		default:
			observedHTTP, observedFinding := false, false
			for _, message := range payload.Messages[1:] {
				if message.Role != "user" {
					continue
				}
				observedHTTP = observedHTTP || strings.Contains(message.Content, `"tool":"http.read"`)
				observedFinding = observedFinding || strings.Contains(message.Content, `"tool":"finding.report"`)
			}
			switch {
			case observedFinding:
				response = `{"summary":"已根据本地工具结果记录待复核漏洞线索"}`
			case observedHTTP:
				input, _ := json.Marshal(map[string]any{"title": "受控样例中的授权线索", "vulnerability_type": "bola", "location": "/", "severity": "high", "asset_ids": []string{assetID}, "impact": "本地样例数据可读", "reproduction_steps": []string{"GET /"}, "observed_result": "返回 controlled private-data fixture", "recommendation": "检查对象权限"})
				response = fmt.Sprintf(`{"tool_calls":[{"name":"finding.report","input":%s}]}`, input)
			default:
				response = fmt.Sprintf(`{"tool_calls":[{"name":"http.read","input":{"url":%q,"method":"GET"}}]}`, target.URL)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": response}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10}})
	}))
	defer backup.Close()
	enabled, stream := true, false
	createProfile := func(name, baseURL, credential string) llm.Profile {
		profile, err := profiles.Create(ctx, operator.OrganizationID, operator.ID, llm.CreateInput{Name: name, Format: "openai-chat", BaseURL: baseURL + "/v1", Model: "phase7-stub", Credential: llm.CredentialWrite{SecretValue: credential}, Enabled: &enabled, Stream: &stream, ContextWindow: 8192, MaxOutputTokens: 512, RatePerSecond: 100, RatePerMinute: 1000})
		if err != nil {
			t.Fatal(err)
		}
		return profile
	}
	first := createProfile("primary", primary.URL, "primary-secret")
	second := createProfile("backup", backup.URL, "backup-secret")
	if err := profiles.SetDefaultChain(ctx, operator.OrganizationID, operator.ID, []string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	scheduler := agent.Scheduler{Engine: agent.Engine{Pool: pool, Profiles: profiles, Router: llm.Router{Store: profiles}}, Tools: &tool.Dispatcher{Pool: pool, DataDir: dataDir, Enforcer: policy.Enforcer{TestOnlyAllowPrivate: true}}}
	handler := NewHandler(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Pool: pool, Secrets: &secrets, Scheduler: &scheduler, Readiness: system.Readiness{Database: pool, DataDir: dataDir}})
	var cookies []*http.Cookie
	var csrf string
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var input io.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(encoded)
		}
		req := httptest.NewRequest(method, path, input)
		req.Host = "localhost"
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" && method != http.MethodGet {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	login := call(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "phase7@example.invalid", "password": "correct horse battery staple"})
	if login.Code != 200 {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	cookies = login.Result().Cookies()
	for _, cookie := range cookies {
		if cookie.Name == csrfCookie {
			csrf = cookie.Value
		}
	}
	created := call(http.MethodPost, "/api/v1/tasks", map[string]any{"title": "Phase 7 本地授权挖掘", "goals": []any{map[string]any{"text": "检查方向 A", "priority": 5}, map[string]any{"text": "检查方向 B", "priority": 5}}, "scope": map[string]any{"entries": []any{map[string]any{"effect": "allow", "kind": "ip", "value": "127.0.0.1", "ports": []int{port}}}}, "execution": map[string]any{"policy_mode": "autonomous", "worker_limit": 3}})
	if created.Code != 201 {
		t.Fatalf("create task: %d %s", created.Code, created.Body.String())
	}
	var item struct {
		Data task.Detail `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &item); err != nil || len(item.Data.Goals) != 2 {
		t.Fatalf("task response: %+v %v", item, err)
	}
	taskID := item.Data.Task.ID
	goalIDs = []string{item.Data.Goals[0].ID, item.Data.Goals[1].ID}
	asset, err := (task.Store{Pool: pool}).UpsertAsset(ctx, operator.OrganizationID, operator.ID, task.AssetInput{Kind: "endpoint", Value: target.URL + "/", SourceTaskID: &taskID})
	if err != nil {
		t.Fatal(err)
	}
	assetID = asset.ID
	started := call(http.MethodPost, "/api/v1/tasks/"+taskID+":start", map[string]any{"version": item.Data.Task.Version})
	if started.Code != 200 {
		t.Fatalf("start task: %d %s", started.Code, started.Body.String())
	}
	planRound := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE planner_wakes SET due_at=now()-interval '1 second' WHERE task_id=$1`, taskID); err != nil {
			t.Fatal(err)
		}
		claim, ok, err := scheduler.ClaimPlanner(ctx)
		if err != nil || !ok {
			t.Fatalf("claim planner: %v %v", ok, err)
		}
		runErr := scheduler.RunPlanner(ctx, claim)
		if finishErr := scheduler.FinishPlanner(ctx, claim, runErr); runErr != nil || finishErr != nil {
			t.Fatalf("planner: run=%v finish=%v", runErr, finishErr)
		}
	}
	planRound()
	claims := make([]agent.Claim, 0, 2)
	for range 2 {
		claim, ok, err := scheduler.ClaimWorker(ctx)
		if err != nil || !ok {
			t.Fatalf("claim worker: %v %v", ok, err)
		}
		claims = append(claims, claim)
	}
	var wg sync.WaitGroup
	errors := make(chan error, len(claims))
	for _, claim := range claims {
		wg.Add(1)
		go func(claim agent.Claim) { defer wg.Done(); errors <- scheduler.RunWorker(ctx, claim) }(claim)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	graph, err := (task.Store{Pool: pool}).Graph(ctx, operator.OrganizationID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.Kind == "fact" && node.SourceIntentID != nil && node.RunID != nil && node.ToolCallID != nil {
			var goalID string
			if err := pool.QueryRow(ctx, `SELECT goal_id FROM task_intents WHERE id=$1`, *node.SourceIntentID).Scan(&goalID); err != nil {
				t.Fatal(err)
			}
			proofs[goalID] = node.ID
		}
	}
	if len(proofs) != 2 {
		t.Fatalf("expected two facts with complete lineage, got %v", proofs)
	}
	if err := scheduler.Wake(ctx, operator.OrganizationID, taskID, "test.proof"); err != nil {
		t.Fatal(err)
	}
	planRound()
	if err := scheduler.Wake(ctx, operator.OrganizationID, taskID, "test.complete"); err != nil {
		t.Fatal(err)
	}
	planRound()
	detail := call(http.MethodGet, "/api/v1/tasks/"+taskID, nil)
	if detail.Code != 200 || !bytes.Contains(detail.Body.Bytes(), []byte(`"status":"completed"`)) {
		t.Fatalf("task did not complete in API: %d %s", detail.Code, detail.Body.String())
	}
	graphResponse := call(http.MethodGet, "/api/v1/tasks/"+taskID+"/graph", nil)
	if graphResponse.Code != 200 || !bytes.Contains(graphResponse.Body.Bytes(), []byte(proofs[goalIDs[0]])) || !bytes.Contains(graphResponse.Body.Bytes(), []byte(proofs[goalIDs[1]])) {
		t.Fatalf("completed graph omitted proof lineage: %d", graphResponse.Code)
	}
	findings, err := (finding.Store{Pool: pool}).List(ctx, operator.OrganizationID, taskID)
	if err != nil || len(findings) == 0 || findings[0].Status != "draft" {
		t.Fatalf("findings: %+v %v", findings, err)
	}
	findingResponse := call(http.MethodGet, "/api/v1/findings?task_id="+taskID, nil)
	if findingResponse.Code != 200 || !bytes.Contains(findingResponse.Body.Bytes(), []byte(findings[0].Title)) {
		t.Fatalf("finding omitted from API: %d %s", findingResponse.Code, findingResponse.Body.String())
	}
	for i := 0; i < 10; i++ {
		count, err := (task.Store{Pool: pool}).ProjectActivities(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
	}
	activityResponse := call(http.MethodGet, "/api/v1/tasks/"+taskID+"/activities?limit=100", nil)
	if activityResponse.Code != 200 || !bytes.Contains(activityResponse.Body.Bytes(), []byte("llm.failover")) {
		t.Fatalf("failover omitted from visible activity: %d %s", activityResponse.Code, activityResponse.Body.String())
	}
	var toolCalls, failovers, blocked, primaryAttempts, backupUsage int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tool_calls WHERE task_id=$1 AND status='succeeded'`, taskID).Scan(&toolCalls); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE task_id=$1 AND event_type='llm.failover'`, taskID).Scan(&failovers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tool_calls WHERE task_id=$1 AND status='blocked'`, taskID).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM llm_attempts WHERE profile_id=$1 AND status='failed'`, first.ID).Scan(&primaryAttempts); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM llm_attempts WHERE profile_id=$1 AND status='succeeded' AND input_tokens=20 AND output_tokens=10 AND estimated=false`, second.ID).Scan(&backupUsage); err != nil {
		t.Fatal(err)
	}
	if targetHits.Load() != 2 || toolCalls < 4 || failovers != 1 || blocked != 0 || primaryCalls.Load() != 1 || backupCalls.Load() < 8 || primaryAttempts != 1 || backupUsage < 8 {
		t.Fatalf("target=%d tools=%d failover=%d blocked=%d primary=%d backup=%d primary_attempts=%d backup_usage=%d", targetHits.Load(), toolCalls, failovers, blocked, primaryCalls.Load(), backupCalls.Load(), primaryAttempts, backupUsage)
	}

	t.Run("evidence survives traffic deletion and is eventually collected", func(t *testing.T) {
		listed := call(http.MethodGet, "/api/v1/traffic?task_id="+taskID, nil)
		var trafficList struct {
			Data []traffic.Entry `json:"data"`
		}
		if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &trafficList) != nil || len(trafficList.Data) != 2 {
			t.Fatalf("captured traffic: %d %s", listed.Code, listed.Body.String())
		}
		findingID := findings[0].ID
		bindingIDs := make([]string, 0, 2)
		responses := make(map[string][]byte)
		for index, exchange := range trafficList.Data {
			original := call(http.MethodGet, "/api/v1/traffic/"+exchange.ID+"/body?part=response", nil)
			if original.Code != 200 || !bytes.Contains(original.Body.Bytes(), []byte("controlled private-data fixture")) {
				t.Fatalf("original response: %d %s", original.Code, original.Body.String())
			}
			role := "baseline"
			if index == 1 {
				role = "proof"
			}
			bound := call(http.MethodPost, "/api/v1/findings/"+findingID+"/evidence", map[string]any{"traffic_id": exchange.ID, "role": role, "note": "受控靶场原始响应"})
			var binding struct {
				Data struct {
					BindingID      string  `json:"binding_id"`
					ResponseSHA256 *string `json:"response_sha256"`
				} `json:"data"`
			}
			if bound.Code != 201 || json.Unmarshal(bound.Body.Bytes(), &binding) != nil || binding.Data.BindingID == "" || binding.Data.ResponseSHA256 == nil {
				t.Fatalf("bind %s: %d %s", role, bound.Code, bound.Body.String())
			}
			bindingIDs = append(bindingIDs, binding.Data.BindingID)
			responses[binding.Data.BindingID] = bytes.Clone(original.Body.Bytes())
		}
		currentFinding := call(http.MethodGet, "/api/v1/findings/"+findingID, nil)
		var findingState struct {
			Data finding.Finding `json:"data"`
		}
		if currentFinding.Code != 200 || json.Unmarshal(currentFinding.Body.Bytes(), &findingState) != nil {
			t.Fatalf("finding with evidence: %d %s", currentFinding.Code, currentFinding.Body.String())
		}
		confirmed := call(http.MethodPost, "/api/v1/findings/"+findingID+":confirm", map[string]any{"version": findingState.Data.Version})
		if confirmed.Code != 200 || !bytes.Contains(confirmed.Body.Bytes(), []byte(`"status":"confirmed"`)) {
			t.Fatalf("confirm finding: %d %s", confirmed.Code, confirmed.Body.String())
		}
		reportState := call(http.MethodGet, "/api/v1/tasks/"+taskID+"/report", nil)
		var reportVersion struct {
			Data struct {
				Version                int64 `json:"version"`
				CurrentEvidenceVersion int64 `json:"current_evidence_version"`
			} `json:"data"`
		}
		if reportState.Code != 200 || json.Unmarshal(reportState.Body.Bytes(), &reportVersion) != nil || reportVersion.Data.CurrentEvidenceVersion == 0 {
			t.Fatalf("report version: %d %s", reportState.Code, reportState.Body.String())
		}
		saved := call(http.MethodPut, "/api/v1/tasks/"+taskID+"/report", map[string]any{"body": "受控靶场证据结论", "version": reportVersion.Data.Version, "evidence_version": reportVersion.Data.CurrentEvidenceVersion})
		if saved.Code != 200 {
			t.Fatalf("save report: %d %s", saved.Code, saved.Body.String())
		}
		for _, exchange := range trafficList.Data {
			deleted := call(http.MethodDelete, "/api/v1/traffic/"+exchange.ID, nil)
			if deleted.Code != 204 {
				t.Fatalf("delete ordinary traffic: %d %s", deleted.Code, deleted.Body.String())
			}
		}
		empty := call(http.MethodGet, "/api/v1/traffic?task_id="+taskID, nil)
		if empty.Code != 200 || !bytes.Contains(empty.Body.Bytes(), []byte(`"data":[]`)) {
			t.Fatalf("ordinary traffic remains: %d %s", empty.Code, empty.Body.String())
		}
		blobStore := traffic.Store{Pool: pool, Blobs: blob.Store{Root: dataDir}}
		if _, err := blobStore.Collect(ctx, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		for _, id := range bindingIDs {
			preserved := call(http.MethodGet, "/api/v1/evidence-bindings/"+id+"/body?part=response", nil)
			if preserved.Code != 200 || !bytes.Equal(preserved.Body.Bytes(), responses[id]) {
				t.Fatalf("evidence %s after deletion: %d %s", id, preserved.Code, preserved.Body.String())
			}
		}
		exported := call(http.MethodGet, "/api/v1/tasks/"+taskID+"/export?format=md-zip", nil)
		if exported.Code != 200 {
			t.Fatalf("export after deletion: %d %s", exported.Code, exported.Body.String())
		}
		archive, err := zip.NewReader(bytes.NewReader(exported.Body.Bytes()), int64(exported.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		var archivedResponses int
		for _, file := range archive.File {
			if !strings.HasSuffix(file.Name, "/response.bin") {
				continue
			}
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			contents, err := io.ReadAll(reader)
			_ = reader.Close()
			if err != nil || !bytes.Contains(contents, []byte("controlled private-data fixture")) {
				t.Fatalf("archived evidence %s: %v", file.Name, err)
			}
			archivedResponses++
		}
		if archivedResponses != 2 {
			t.Fatalf("expected two archived responses, got %d", archivedResponses)
		}
		responseHash := *trafficList.Data[0].ResponseSHA256
		for _, id := range bindingIDs {
			unbound := call(http.MethodDelete, "/api/v1/evidence-bindings/"+id, nil)
			if unbound.Code != 204 {
				t.Fatalf("unbind evidence: %d %s", unbound.Code, unbound.Body.String())
			}
		}
		if _, err := blobStore.Collect(ctx, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		blobPath := filepath.Join(dataDir, "blobs", responseHash[:2], responseHash)
		if _, err := os.Stat(blobPath); !os.IsNotExist(err) {
			t.Fatalf("unreferenced evidence blob remains: %v", err)
		}
		stale := call(http.MethodGet, "/api/v1/tasks/"+taskID+"/report", nil)
		if stale.Code != 200 || !bytes.Contains(stale.Body.Bytes(), []byte(`"stale":true`)) {
			t.Fatalf("report did not become stale after unbinding: %d %s", stale.Code, stale.Body.String())
		}
	})
}
