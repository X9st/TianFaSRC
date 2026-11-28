package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/X9st/TianFaSRC/internal/mcpclient"
	"github.com/X9st/TianFaSRC/internal/policy"
	"github.com/X9st/TianFaSRC/internal/secret"
	"github.com/X9st/TianFaSRC/internal/store/postgres"
	"github.com/X9st/TianFaSRC/internal/system"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/tool"
	"github.com/X9st/TianFaSRC/internal/traffic"
	"github.com/chromedp/chromedp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	var targetHits, primaryCalls, backupCalls, plannerPlans atomic.Int32
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
			switch plannerPlans.Add(1) {
			case 1:
				response = fmt.Sprintf(`{"todos":[{"key":"root","title":"先验证方向 A","depends_on":[]},{"key":"child","title":"再验证下游","depends_on":["root"]}],"intents":[{"goal_id":%q,"objective":"检查方向 A","success_criteria":"本地 HTTP 观察","todo_key":"root"},{"goal_id":%q,"objective":"检查方向 B","success_criteria":"本地 HTTP 观察"},{"goal_id":%q,"objective":"验证串行下游","success_criteria":"前置 Fact 后的 HTTP 观察","todo_key":"child"}],"planning_blocked":""}`, goalIDs[0], goalIDs[1], goalIDs[0])
			case 2:
				response = fmt.Sprintf(`{"todos":[{"key":"root","title":"先验证方向 A","depends_on":[]},{"key":"child","title":"再验证下游","depends_on":["root"]}],"intents":[{"goal_id":%q,"objective":"验证串行下游","success_criteria":"前置 Fact 后的 HTTP 观察","todo_key":"child"}],"planning_blocked":""}`, goalIDs[0])
			default:
				response = fmt.Sprintf(`{"proofs":[{"goal_id":%q,"evidence_node_ids":[%q]},{"goal_id":%q,"evidence_node_ids":[%q]}]}`, goalIDs[0], proofs[goalIDs[0]], goalIDs[1], proofs[goalIDs[1]])
			}
		default:
			observedHTTP, observedFinding, observedAsset := false, false, false
			firstDirection := len(payload.Messages) > 1 && (strings.Contains(payload.Messages[1].Content, "检查方向 A") || strings.Contains(payload.Messages[1].Content, "检查 方向 A"))
			for _, message := range payload.Messages[1:] {
				if message.Role != "user" {
					continue
				}
				observedHTTP = observedHTTP || strings.Contains(message.Content, `"tool":"http.read"`)
				observedFinding = observedFinding || strings.Contains(message.Content, `"tool":"finding.report"`)
				observedAsset = observedAsset || strings.Contains(message.Content, `"tool":"asset.upsert"`)
			}
			switch {
			case observedFinding:
				response = `{"summary":"已根据本地工具结果记录待复核漏洞线索"}`
			case firstDirection && observedHTTP && !observedAsset:
				response = fmt.Sprintf(`{"tool_calls":[{"name":"asset.upsert","input":{"kind":"endpoint","value":%q}}]}`, target.URL+"/new")
			case observedHTTP:
				input, _ := json.Marshal(map[string]any{"title": "受控样例中的授权线索", "vulnerability_type": "bola", "location": "/", "severity": "high", "asset_ids": []string{assetID}, "impact": "本地样例数据可读", "reproduction_steps": []string{"GET /"}, "observed_result": "返回 controlled private-data fixture", "recommendation": "检查对象权限"})
				response = fmt.Sprintf(`{"tool_calls":[{"name":"finding.report","input":%s}]}`, input)
			default:
				url := target.URL
				if firstDirection {
					url += "/new"
				}
				response = fmt.Sprintf(`{"tool_calls":[{"name":"http.read","input":{"url":%q,"method":"GET"}}]}`, url)
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
	browserEnabled := os.Getenv("TIANFA_BROWSER_E2E") == "1"
	var browserCtx context.Context
	var liveBrowserURL string
	if browserEnabled {
		browserCtx = browserContext(t)
		live := httptest.NewServer(handler)
		liveBrowserURL = live.URL
		defer func() { live.CloseClientConnections(); live.Close() }()
		if err := chromedp.Do(browserCtx, chromedp.Navigate(live.URL+"/login/"), chromedp.WaitVisible(chromedp.CSS("input[type=email]")), chromedp.SendKeys(chromedp.CSS("input[type=email]"), "phase7@example.invalid"), chromedp.SendKeys(chromedp.CSS("input[type=password]"), "correct horse battery staple"), chromedp.Click(chromedp.CSS("button[type=submit]"))); err != nil {
			t.Fatalf("browser login: %v", err)
		}
		if err := browserWaitText(browserCtx, "挖掘任务"); err != nil {
			t.Fatal(err)
		}
	}
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
	var item struct {
		Data task.Detail `json:"data"`
	}
	if browserEnabled {
		if err := browserClickButton(browserCtx, "新建任务"); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Do(browserCtx, chromedp.WaitVisible(chromedp.CSS(".task-form")), chromedp.SendKeys(chromedp.CSS(".task-form input[placeholder^='例如：测试环境']"), "Phase 7 本地授权挖掘"), chromedp.SendKeys(chromedp.CSS("input[aria-label='挖掘目标 1']"), "检查方向 A")); err != nil {
			t.Fatal(err)
		}
		if err := browserClickButton(browserCtx, "添加目标"); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Do(browserCtx, chromedp.WaitVisible(chromedp.CSS("input[aria-label='挖掘目标 2']")), chromedp.SendKeys(chromedp.CSS("input[aria-label='挖掘目标 2']"), "检查方向 B")); err != nil {
			t.Fatal(err)
		}
		if err := browserSelect(browserCtx, ".task-form .scope-row label:nth-child(2) select", "ip"); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Do(browserCtx, chromedp.SendKeys(chromedp.CSS(".task-form .scope-row label:nth-child(3) input"), "127.0.0.1"), chromedp.SendKeys(chromedp.CSS(".task-form .scope-row label:nth-child(4) input"), strconv.Itoa(port))); err != nil {
			t.Fatal(err)
		}
		if err := browserClickButton(browserCtx, "创建草稿"); err != nil {
			t.Fatal(err)
		}
		if err := browserWaitText(browserCtx, "挖掘任务详情"); err != nil {
			t.Fatal(err)
		}
		var createdID string
		if err := pool.QueryRow(ctx, `SELECT id FROM tasks WHERE title='Phase 7 本地授权挖掘'`).Scan(&createdID); err != nil {
			t.Fatal(err)
		}
		item.Data, err = (task.Store{Pool: pool}).Get(ctx, operator.OrganizationID, createdID)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		created := call(http.MethodPost, "/api/v1/tasks", map[string]any{"title": "Phase 7 本地授权挖掘", "goals": []any{map[string]any{"text": "检查方向 A", "priority": 5}, map[string]any{"text": "检查方向 B", "priority": 5}}, "scope": map[string]any{"entries": []any{map[string]any{"effect": "allow", "kind": "ip", "value": "127.0.0.1", "ports": []int{port}}}}, "execution": map[string]any{"policy_mode": "autonomous", "worker_limit": 3}})
		if created.Code != 201 {
			t.Fatalf("create task: %d %s", created.Code, created.Body.String())
		}
		if err := json.Unmarshal(created.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
	}
	if len(item.Data.Goals) != 2 {
		t.Fatalf("task goals=%d", len(item.Data.Goals))
	}
	taskID := item.Data.Task.ID
	goalIDs = []string{item.Data.Goals[0].ID, item.Data.Goals[1].ID}
	asset, err := (task.Store{Pool: pool}).UpsertAsset(ctx, operator.OrganizationID, operator.ID, task.AssetInput{Kind: "endpoint", Value: target.URL + "/", SourceTaskID: &taskID})
	if err != nil {
		t.Fatal(err)
	}
	assetID = asset.ID
	if browserEnabled {
		if err := browserClickButton(browserCtx, "启动任务"); err != nil {
			t.Fatal(err)
		}
		if err := browserWaitText(browserCtx, "排队中"); err != nil {
			t.Fatal(err)
		}
	} else {
		started := call(http.MethodPost, "/api/v1/tasks/"+taskID+":start", map[string]any{"version": item.Data.Task.Version})
		if started.Code != 200 {
			t.Fatalf("start task: %d %s", started.Code, started.Body.String())
		}
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
	if _, err := pool.Exec(ctx, `UPDATE planner_wakes SET due_at=now()-interval '1 second' WHERE task_id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	firstPlanner, ok, err := scheduler.ClaimPlanner(ctx)
	if err != nil || !ok {
		t.Fatalf("first planner claim: %v %v", ok, err)
	}
	firstPlanResult := make(chan error, 1)
	go func() {
		runErr := scheduler.RunPlanner(ctx, firstPlanner)
		finishErr := scheduler.FinishPlanner(ctx, firstPlanner, runErr)
		if runErr != nil {
			firstPlanResult <- runErr
		} else {
			firstPlanResult <- finishErr
		}
	}()
	humanIntent := call(http.MethodPost, "/api/v1/tasks/"+taskID+"/conversation", map[string]any{"action": "intent", "text": "检查 方向 A", "goal_id": goalIDs[0], "success_criteria": "本地 HTTP 观察"})
	if humanIntent.Code != 201 && (humanIntent.Code != 409 || !bytes.Contains(humanIntent.Body.Bytes(), []byte("duplicate-intent"))) {
		t.Fatalf("human/planner direction race: %d %s", humanIntent.Code, humanIntent.Body.String())
	}
	if err := <-firstPlanResult; err != nil {
		t.Fatalf("concurrent planner: %v", err)
	}
	var directionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM task_intents WHERE task_id=$1`, taskID).Scan(&directionCount); err != nil || directionCount != 2 {
		t.Fatalf("similar planner/human intents=%d err=%v", directionCount, err)
	}
	claims := make([]agent.Claim, 0, 2)
	for range 2 {
		claim, ok, err := scheduler.ClaimWorker(ctx)
		if err != nil || !ok {
			t.Fatalf("claim worker: %v %v", ok, err)
		}
		claims = append(claims, claim)
	}
	var wg sync.WaitGroup
	workerResults := make(chan error, len(claims))
	for _, claim := range claims {
		wg.Add(1)
		go func(claim agent.Claim) { defer wg.Done(); workerResults <- scheduler.RunWorker(ctx, claim) }(claim)
	}
	wg.Wait()
	close(workerResults)
	for err := range workerResults {
		if err != nil {
			t.Fatal(err)
		}
	}
	todos, err := scheduler.ListTodos(ctx, operator.OrganizationID, taskID)
	todoStatuses := make(map[string]string, len(todos))
	for _, item := range todos {
		todoStatuses[item.Title] = item.Status
	}
	if err != nil || len(todos) != 2 || todoStatuses["先验证方向 A"] != "done" || todoStatuses["再验证下游"] != "open" {
		t.Fatalf("serial Todo after first workers: %+v %v", todos, err)
	}
	if _, ready, err := scheduler.ClaimWorker(ctx); err != nil || ready {
		t.Fatalf("dependent intent claimed before planning: %v %v", ready, err)
	}
	if err := scheduler.Wake(ctx, operator.OrganizationID, taskID, "test.todo_dependency"); err != nil {
		t.Fatal(err)
	}
	planRound()
	childClaim, ready, err := scheduler.ClaimWorker(ctx)
	if err != nil || !ready || childClaim.Objective != "验证串行下游" {
		t.Fatalf("dependent intent not dispatched: %+v %v %v", childClaim, ready, err)
	}
	if err := scheduler.RunWorker(ctx, childClaim); err != nil {
		t.Fatal(err)
	}
	todos, err = scheduler.ListTodos(ctx, operator.OrganizationID, taskID)
	if err != nil || len(todos) != 2 || todos[0].Status != "done" || todos[1].Status != "done" {
		t.Fatalf("serial Todo completion: %+v %v", todos, err)
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
	var newAssetID string
	if err := pool.QueryRow(ctx, `SELECT a.id FROM assets a JOIN task_asset_links l ON l.asset_id=a.id JOIN exploration_anchors anchor ON anchor.asset_id=a.id JOIN task_intents i ON i.id=anchor.node_id WHERE l.task_id=$1 AND a.kind='endpoint' AND a.display_value=$2 AND i.task_id=$1 AND i.goal_id=$3`, taskID, target.URL+"/new", goalIDs[0]).Scan(&newAssetID); err != nil {
		t.Fatalf("worker asset lineage missing: %v", err)
	}
	blockedIntent := identity.NewID()
	if _, err := pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'intent','open','验证越界提示')`, blockedIntent, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key) VALUES($1,$2,$3,'验证越界提示','程序阻断', $4)`, blockedIntent, taskID, goalIDs[0], "phase7-out-of-scope"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind) VALUES($1,$2,$3,'spawns')`, taskID, goalIDs[0], blockedIntent); err != nil {
		t.Fatal(err)
	}
	blockedClaim, ok, err := scheduler.ClaimWorker(ctx)
	if err != nil || !ok || blockedClaim.IntentID != blockedIntent {
		t.Fatalf("claim scope direction: %+v %v %v", blockedClaim, ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE task_intents SET status='running' WHERE id=$1`, blockedIntent); err != nil {
		t.Fatal(err)
	}
	outsideURL := fmt.Sprintf("http://127.0.0.2:%d/private", port)
	outsideInput, _ := json.Marshal(map[string]any{"url": outsideURL, "method": "GET"})
	blockedResult, blockedErr := scheduler.Tools.Execute(ctx, tool.Call{OrganizationID: operator.OrganizationID, TaskID: taskID, IntentID: blockedIntent, RunID: blockedClaim.RunID, ActorID: operator.ID, Name: "http.read", Input: outsideInput, IdempotencyKey: "phase7-browser-outside-scope"})
	if !errors.Is(blockedErr, tool.ErrToolBlocked) || blockedResult.Status != "blocked" {
		t.Fatalf("outside scope: %+v %v", blockedResult, blockedErr)
	}
	if err := scheduler.StopIntent(ctx, operator.OrganizationID, operator.ID, taskID, blockedIntent, "越界验证已记录"); err != nil {
		t.Fatal(err)
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
	if browserEnabled {
		if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/tasks/?id="+taskID), chromedp.WaitVisible(chromedp.CSS(".task-summary"))); err != nil {
			t.Fatal(err)
		}
		for _, visible := range []string{"已完成", "llm.failover", "检查方向 A", "检查方向 B", "再验证下游", "asset.upsert", "http.read"} {
			if err := browserWaitText(browserCtx, visible); err != nil {
				t.Fatal(err)
			}
		}
		if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/findings/?task="+taskID+"&tab=findings"), chromedp.WaitVisible(chromedp.CSS(".phase5-tabs"))); err != nil {
			t.Fatal(err)
		}
		if err := browserWaitText(browserCtx, findings[0].Title); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/system/"), chromedp.WaitVisible(chromedp.CSS(".phase6-audit"))); err != nil {
			t.Fatal(err)
		}
		if err := browserWaitText(browserCtx, "Phase 7 本地授权挖掘"); err != nil {
			t.Fatal(err)
		}
		if err := browserSelect(browserCtx, ".phase6-columns select", taskID); err != nil {
			t.Fatal(err)
		}
		for _, visible := range []string{"tool.policy_blocked", "http.read"} {
			if err := browserWaitText(browserCtx, visible); err != nil {
				t.Fatal(err)
			}
		}
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
	if targetHits.Load() != 3 || toolCalls < 7 || failovers != 1 || blocked != 1 || primaryCalls.Load() != 1 || backupCalls.Load() < 12 || primaryAttempts != 1 || backupUsage < 12 {
		t.Fatalf("target=%d tools=%d failover=%d blocked=%d primary=%d backup=%d primary_attempts=%d backup_usage=%d", targetHits.Load(), toolCalls, failovers, blocked, primaryCalls.Load(), backupCalls.Load(), primaryAttempts, backupUsage)
	}

	t.Run("evidence survives traffic deletion and is eventually collected", func(t *testing.T) {
		listed := call(http.MethodGet, "/api/v1/traffic?task_id="+taskID, nil)
		var trafficList struct {
			Data []traffic.Entry `json:"data"`
		}
		if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &trafficList) != nil || len(trafficList.Data) != 3 {
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
			var binding struct {
				Data struct {
					BindingID      string  `json:"binding_id"`
					ResponseSHA256 *string `json:"response_sha256"`
				} `json:"data"`
			}
			if browserEnabled && index == 1 {
				if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/findings/?task="+taskID+"&tab=findings"), chromedp.WaitVisible(chromedp.CSS(".phase5-tabs"))); err != nil {
					t.Fatal(err)
				}
				if err := browserClickButton(browserCtx, findings[0].Title); err != nil {
					t.Fatal(err)
				}
				if err := browserWaitText(browserCtx, "绑定流量证据"); err != nil {
					t.Fatal(err)
				}
				if err := browserSelect(browserCtx, ".phase5-columns section:nth-child(2) form.workspace-form label:nth-of-type(1) select", exchange.ID); err != nil {
					t.Fatal(err)
				}
				if err := browserClickButton(browserCtx, "绑定证据"); err != nil {
					t.Fatal(err)
				}
				if err := browserWaitText(browserCtx, "流量已保存为不可变证据"); err != nil {
					t.Fatal(err)
				}
				listed := call(http.MethodGet, "/api/v1/findings/"+findingID+"/evidence", nil)
				var snapshot struct {
					Data []struct {
						BindingID      string  `json:"binding_id"`
						ResponseSHA256 *string `json:"response_sha256"`
						Role           string  `json:"role"`
					} `json:"data"`
				}
				if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &snapshot) != nil {
					t.Fatalf("browser-bound evidence: %d %s", listed.Code, listed.Body.String())
				}
				for _, item := range snapshot.Data {
					if item.Role == "proof" {
						binding.Data.BindingID, binding.Data.ResponseSHA256 = item.BindingID, item.ResponseSHA256
					}
				}
			} else {
				bound := call(http.MethodPost, "/api/v1/findings/"+findingID+"/evidence", map[string]any{"traffic_id": exchange.ID, "role": role, "note": "受控靶场原始响应"})
				if bound.Code != 201 || json.Unmarshal(bound.Body.Bytes(), &binding) != nil {
					t.Fatalf("bind %s: %d %s", role, bound.Code, bound.Body.String())
				}
			}
			if binding.Data.BindingID == "" || binding.Data.ResponseSHA256 == nil {
				t.Fatalf("missing %s evidence snapshot after binding", role)
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
		if browserEnabled {
			if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/findings/?task="+taskID+"&tab=findings"), chromedp.WaitVisible(chromedp.CSS(".phase5-tabs"))); err != nil {
				t.Fatal(err)
			}
			if err := browserClickButton(browserCtx, findings[0].Title); err != nil {
				t.Fatal(err)
			}
			if err := browserWaitText(browserCtx, "确认漏洞"); err != nil {
				t.Fatal(err)
			}
			if err := browserClickButton(browserCtx, "确认漏洞"); err != nil {
				t.Fatal(err)
			}
			if err := browserWaitText(browserCtx, "漏洞已确认"); err != nil {
				body, _ := chromedp.Run(browserCtx, chromedp.Evaluate[string](`document.body.innerText`))
				t.Fatalf("%v; browser body: %s", err, body)
			}
		} else {
			confirmed := call(http.MethodPost, "/api/v1/findings/"+findingID+":confirm", map[string]any{"version": findingState.Data.Version})
			if confirmed.Code != 200 || !bytes.Contains(confirmed.Body.Bytes(), []byte(`"status":"confirmed"`)) {
				t.Fatalf("confirm finding: %d %s", confirmed.Code, confirmed.Body.String())
			}
		}
		confirmed := call(http.MethodGet, "/api/v1/findings/"+findingID, nil)
		if confirmed.Code != 200 || !bytes.Contains(confirmed.Body.Bytes(), []byte(`"status":"confirmed"`)) {
			t.Fatalf("confirm finding: %d %s", confirmed.Code, confirmed.Body.String())
		}
		if browserEnabled {
			if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/findings/?task="+taskID+"&tab=findings"), chromedp.WaitVisible(chromedp.CSS(".phase5-tabs"))); err != nil {
				t.Fatal(err)
			}
			for _, visible := range []string{findings[0].Title, "已确认"} {
				if err := browserWaitText(browserCtx, visible); err != nil {
					t.Fatal(err)
				}
			}
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
		if browserEnabled {
			if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/findings/?task="+taskID+"&tab=report"), chromedp.WaitVisible(chromedp.CSS(".phase5-tabs")), chromedp.WaitVisible(chromedp.CSS(".phase5-columns textarea")), chromedp.SendKeys(chromedp.CSS(".phase5-columns textarea"), "受控靶场证据结论")); err != nil {
				t.Fatal(err)
			}
			if err := browserClickButton(browserCtx, "保存报告"); err != nil {
				t.Fatal(err)
			}
			if err := browserWaitText(browserCtx, "报告已按当前证据版本保存"); err != nil {
				t.Fatal(err)
			}
		} else {
			saved := call(http.MethodPut, "/api/v1/tasks/"+taskID+"/report", map[string]any{"body": "受控靶场证据结论", "version": reportVersion.Data.Version, "evidence_version": reportVersion.Data.CurrentEvidenceVersion})
			if saved.Code != 200 {
				t.Fatalf("save report: %d %s", saved.Code, saved.Body.String())
			}
		}
		saved := call(http.MethodGet, "/api/v1/tasks/"+taskID+"/report", nil)
		if saved.Code != 200 || !bytes.Contains(saved.Body.Bytes(), []byte("受控靶场证据结论")) {
			t.Fatalf("report body after save: %d %s", saved.Code, saved.Body.String())
		}
		if browserEnabled {
			if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/findings/?task="+taskID+"&tab=traffic"), chromedp.WaitVisible(chromedp.CSS(".phase5-tabs")), chromedp.WaitVisible(chromedp.CSS(".phase5-columns section:nth-child(1) .phase5-list button"))); err != nil {
				t.Fatal(err)
			}
			for remaining := len(trafficList.Data); remaining > 0; remaining-- {
				if err := chromedp.Do(browserCtx, chromedp.Click(chromedp.CSS(".phase5-columns section:nth-child(1) .phase5-list button:first-child"))); err != nil {
					t.Fatal(err)
				}
				if err := browserClickButton(browserCtx, "删除普通流量"); err != nil {
					t.Fatal(err)
				}
				if err := browserWaitCount(browserCtx, ".phase5-columns section:nth-child(1) .phase5-list button", remaining-1); err != nil {
					t.Fatal(err)
				}
			}
		} else {
			for _, exchange := range trafficList.Data {
				deleted := call(http.MethodDelete, "/api/v1/traffic/"+exchange.ID, nil)
				if deleted.Code != 204 {
					t.Fatalf("delete ordinary traffic: %d %s", deleted.Code, deleted.Body.String())
				}
			}
		}
		empty := call(http.MethodGet, "/api/v1/traffic?task_id="+taskID, nil)
		if empty.Code != 200 || !bytes.Contains(empty.Body.Bytes(), []byte(`"data":[]`)) {
			t.Fatalf("ordinary traffic remains: %d %s", empty.Code, empty.Body.String())
		}
		if browserEnabled {
			if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/findings/?task="+taskID+"&tab=report"), chromedp.WaitVisible(chromedp.CSS(".phase5-tabs"))); err != nil {
				t.Fatal(err)
			}
			if err := browserWaitText(browserCtx, "报告与当前证据一致"); err != nil {
				t.Fatal(err)
			}
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
		if archivedResponses != len(trafficList.Data) {
			t.Fatalf("expected %d archived responses, got %d", len(trafficList.Data), archivedResponses)
		}
		if browserEnabled {
			if err := browserClickButton(browserCtx, "报告与原始证据 ZIP"); err != nil {
				t.Fatal(err)
			}
		}
		responseHash := *trafficList.Data[0].ResponseSHA256
		if browserEnabled {
			if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/findings/?task="+taskID+"&tab=findings"), chromedp.WaitVisible(chromedp.CSS(".phase5-tabs"))); err != nil {
				t.Fatal(err)
			}
			if err := browserClickButton(browserCtx, findings[0].Title); err != nil {
				t.Fatal(err)
			}
			if err := browserWaitCount(browserCtx, ".phase5-evidence", len(bindingIDs)); err != nil {
				t.Fatal(err)
			}
			for remaining := len(bindingIDs); remaining > 0; remaining-- {
				if err := browserClickButton(browserCtx, "解除绑定"); err != nil {
					t.Fatal(err)
				}
				if err := browserWaitCount(browserCtx, ".phase5-evidence", remaining-1); err != nil {
					t.Fatal(err)
				}
			}
		} else {
			for _, id := range bindingIDs {
				unbound := call(http.MethodDelete, "/api/v1/evidence-bindings/"+id, nil)
				if unbound.Code != 204 {
					t.Fatalf("unbind evidence: %d %s", unbound.Code, unbound.Body.String())
				}
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
		if browserEnabled {
			if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/findings/?task="+taskID+"&tab=report"), chromedp.WaitVisible(chromedp.CSS(".phase5-tabs"))); err != nil {
				t.Fatal(err)
			}
			if err := browserWaitText(browserCtx, "证据已变化，旧报告过期"); err != nil {
				t.Fatal(err)
			}
		}
	})
	if browserEnabled {
		if _, err := pool.Exec(ctx, `UPDATE planner_wakes SET owner_id=$2,pending=false,expires_at=now()-interval '1 second' WHERE task_id=$1`, taskID, identity.NewID()); err != nil {
			t.Fatal(err)
		}
		if _, err := scheduler.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := (task.Store{Pool: pool}).ProjectActivities(ctx, 100); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/tasks/?id="+taskID), chromedp.WaitVisible(chromedp.CSS(".task-summary"))); err != nil {
			t.Fatal(err)
		}
		if err := browserWaitText(browserCtx, "planner.reconciled"); err != nil {
			t.Fatal(err)
		}
		mcpServer := mcp.NewServer(&mcp.Implementation{Name: "browser-fixture", Version: "1.0"}, nil)
		mcp.AddTool(mcpServer, &mcp.Tool{Name: "navigate", Description: "read a controlled URL"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct {
			URL  string `json:"url"`
			Note string `json:"note"`
		}) (*mcp.CallToolResult, any, error) {
			result := args.URL
			if args.Note == "large" {
				result = strings.Repeat("evidence-", 4096)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: result}}}, nil, nil
		})
		var disconnectMCP atomic.Bool
		mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, nil)
		mcpHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if disconnectMCP.Load() {
				http.Error(w, "temporary MCP disconnect", http.StatusServiceUnavailable)
				return
			}
			mcpHandler.ServeHTTP(w, r)
		}))
		defer mcpHTTP.Close()
		mcpStore := mcpclient.Store{Pool: pool, Enforcer: policy.Enforcer{TestOnlyAllowPrivate: true}, TestOnlyAllowHTTPURL: true}
		if _, err := mcpStore.Create(ctx, operator.OrganizationID, operator.ID, mcpclient.Server{Name: "browser-fixture", Transport: "streamable_http", Endpoint: mcpHTTP.URL}); err != nil {
			t.Fatal(err)
		}
		discovered, err := mcpStore.Discover(ctx, operator.OrganizationID, "browser-fixture")
		if err != nil || len(discovered) != 1 || discovered[0].Name != "navigate" {
			t.Fatalf("browser MCP discovery: %+v %v", discovered, err)
		}
		if err := mcpStore.SetEnabled(ctx, operator.OrganizationID, operator.ID, "browser-fixture", true); err != nil {
			t.Fatal(err)
		}
		if err := mcpStore.SetToolEnabled(ctx, operator.OrganizationID, operator.ID, "browser-fixture", "navigate", true); err != nil {
			t.Fatal(err)
		}
		var skillArchive bytes.Buffer
		zipWriter := zip.NewWriter(&skillArchive)
		file, err := zipWriter.Create("SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.Write([]byte("---\nname: browser-fixture\nversion: 1.0.0\ndescription: Controlled Browser MCP fixture\nagents: [worker]\nmcps: [browser-fixture]\n---\nRead controlled pages through the declared Browser MCP."))
		if err := zipWriter.Close(); err != nil {
			t.Fatal(err)
		}
		archivePath := filepath.Join(t.TempDir(), "browser-fixture.zip")
		if err := os.WriteFile(archivePath, skillArchive.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/tools/"), chromedp.WaitVisible(chromedp.CSS(".tool-binding-list"))); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Do(browserCtx, chromedp.SetUploadFiles(chromedp.CSS(".tool-upload input[type=file]"), []string{archivePath})); err != nil {
			t.Fatal(err)
		}
		if err := browserWaitText(browserCtx, "Skill 已安装"); err != nil {
			t.Fatal(err)
		}
		for _, visible := range []string{"browser-fixture", "navigate", "延迟解锁 MCP"} {
			if err := browserWaitText(browserCtx, visible); err != nil {
				t.Fatal(err)
			}
		}
		mcpTask, err := (task.Store{Pool: pool}).Create(ctx, operator.OrganizationID, operator.ID, task.CreateInput{Title: "Browser MCP 验收任务", Goals: []task.GoalInput{{Text: "验证受控页面", Priority: 5}}, Scope: struct {
			Entries []task.ScopeEntryInput `json:"entries"`
		}{Entries: []task.ScopeEntryInput{{Effect: "allow", Kind: "ip", Value: "127.0.0.1", Ports: []int{port}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE tasks SET status='running' WHERE id=$1`, mcpTask.Task.ID); err != nil {
			t.Fatal(err)
		}
		mcpIntentID := identity.NewID()
		if _, err := pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'intent','open','受控 Browser MCP 验证')`, mcpIntentID, mcpTask.Task.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key) VALUES($1,$2,$3,'受控 Browser MCP 验证','读取受控页面','phase7-browser-mcp')`, mcpIntentID, mcpTask.Task.ID, mcpTask.Goals[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind) VALUES($1,$2,$3,'spawns')`, mcpTask.Task.ID, mcpTask.Goals[0].ID, mcpIntentID); err != nil {
			t.Fatal(err)
		}
		mcpClaim, ok, err := scheduler.ClaimWorker(ctx)
		if err != nil || !ok || mcpClaim.IntentID != mcpIntentID {
			t.Fatalf("MCP worker claim: %+v %v %v", mcpClaim, ok, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE task_intents SET status='running' WHERE id=$1`, mcpIntentID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE worker_run_budgets SET max_tool_calls=12 WHERE run_id=$1`, mcpClaim.RunID); err != nil {
			t.Fatal(err)
		}
		before, err := scheduler.Tools.AvailableForRun(ctx, operator.OrganizationID, mcpClaim.RunID)
		if err != nil || strings.Contains(strings.Join(before, ","), "mcp__browser-fixture__navigate") {
			t.Fatalf("MCP tool visible before load: %v %v", before, err)
		}
		loaded, err := scheduler.Tools.Execute(ctx, tool.Call{OrganizationID: operator.OrganizationID, TaskID: mcpTask.Task.ID, IntentID: mcpIntentID, RunID: mcpClaim.RunID, ActorID: operator.ID, Name: "skill.load", Input: json.RawMessage(`{"name":"browser-fixture"}`), IdempotencyKey: "phase7-browser-skill-load"})
		if err != nil || loaded.Status != "succeeded" {
			t.Fatalf("load Browser MCP Skill: %+v %v", loaded, err)
		}
		after, err := scheduler.Tools.AvailableForRun(ctx, operator.OrganizationID, mcpClaim.RunID)
		if err != nil || !strings.Contains(strings.Join(after, ","), "mcp__browser-fixture__navigate") {
			t.Fatalf("Browser MCP tool unavailable after load: %v %v", after, err)
		}
		mcpInput, _ := json.Marshal(map[string]string{"url": target.URL, "note": "normal"})
		mcpCall, err := scheduler.Tools.Execute(ctx, tool.Call{OrganizationID: operator.OrganizationID, TaskID: mcpTask.Task.ID, IntentID: mcpIntentID, RunID: mcpClaim.RunID, ActorID: operator.ID, Name: "mcp__browser-fixture__navigate", Input: mcpInput, IdempotencyKey: "phase7-browser-mcp-navigate"})
		if err != nil || mcpCall.Status != "succeeded" {
			t.Fatalf("Browser MCP navigate: %+v %v", mcpCall, err)
		}
		disconnectMCP.Store(true)
		failedMCP, err := scheduler.Tools.Execute(ctx, tool.Call{OrganizationID: operator.OrganizationID, TaskID: mcpTask.Task.ID, IntentID: mcpIntentID, RunID: mcpClaim.RunID, ActorID: operator.ID, Name: "mcp__browser-fixture__navigate", Input: mcpInput, IdempotencyKey: "phase7-browser-mcp-disconnect"})
		if err == nil || failedMCP.Status != "failed" {
			t.Fatalf("Browser MCP disconnect: %+v %v", failedMCP, err)
		}
		disconnectMCP.Store(false)
		reconnected, err := scheduler.Tools.Execute(ctx, tool.Call{OrganizationID: operator.OrganizationID, TaskID: mcpTask.Task.ID, IntentID: mcpIntentID, RunID: mcpClaim.RunID, ActorID: operator.ID, Name: "mcp__browser-fixture__navigate", Input: mcpInput, IdempotencyKey: "phase7-browser-mcp-reconnect"})
		if err != nil || reconnected.Status != "succeeded" {
			t.Fatalf("Browser MCP reconnect: %+v %v", reconnected, err)
		}
		largeInput, _ := json.Marshal(map[string]string{"url": target.URL, "note": "large"})
		largeMCP, err := scheduler.Tools.Execute(ctx, tool.Call{OrganizationID: operator.OrganizationID, TaskID: mcpTask.Task.ID, IntentID: mcpIntentID, RunID: mcpClaim.RunID, ActorID: operator.ID, Name: "mcp__browser-fixture__navigate", Input: largeInput, IdempotencyKey: "phase7-browser-mcp-large"})
		if err != nil || largeMCP.Status != "succeeded" || bytes.Contains(largeMCP.Output, []byte(strings.Repeat("evidence-", 4096))) {
			t.Fatalf("Browser MCP output preview: %+v %v", largeMCP, err)
		}
		fullOutput, _, err := scheduler.Tools.ReadOutput(ctx, operator.OrganizationID, largeMCP.ID)
		if err != nil || !bytes.Contains(fullOutput, []byte(strings.Repeat("evidence-", 4096))) {
			t.Fatalf("Browser MCP full output: %d bytes %v", len(fullOutput), err)
		}
		outsideMCP, _ := json.Marshal(map[string]string{"url": outsideURL, "note": "normal"})
		mcpBlocked, err := scheduler.Tools.Execute(ctx, tool.Call{OrganizationID: operator.OrganizationID, TaskID: mcpTask.Task.ID, IntentID: mcpIntentID, RunID: mcpClaim.RunID, ActorID: operator.ID, Name: "mcp__browser-fixture__navigate", Input: outsideMCP, IdempotencyKey: "phase7-browser-mcp-outside"})
		if !errors.Is(err, tool.ErrToolBlocked) || mcpBlocked.Status != "blocked" {
			t.Fatalf("Browser MCP outside scope: %+v %v", mcpBlocked, err)
		}
		if err := chromedp.Do(browserCtx, chromedp.Navigate(liveBrowserURL+"/tasks/?id="+mcpTask.Task.ID), chromedp.WaitVisible(chromedp.CSS(".task-summary"))); err != nil {
			t.Fatal(err)
		}
		for _, visible := range []string{"Browser MCP 验收任务", "skill.load", "mcp__browser-fixture__navigate", "策略阻断"} {
			if err := browserWaitText(browserCtx, visible); err != nil {
				t.Fatal(err)
			}
		}
		runPhase7MCPWorkerSequence(t, ctx, pool, scheduler, profiles, operator.OrganizationID, operator.ID, target.URL, port, browserCtx, liveBrowserURL)
		runPhase7CrashSequence(t, ctx, pool, scheduler, operator.OrganizationID, operator.ID, dataDir, browserCtx, liveBrowserURL)
		runPhase7ScopeBrowserSequence(t, ctx, pool, scheduler, profiles, operator.OrganizationID, operator.ID, browserCtx, liveBrowserURL)
		runPhase7RateLimitBrowserSequence(t, ctx, pool, scheduler, profiles, operator.OrganizationID, operator.ID, target.URL, port, &targetHits, browserCtx, liveBrowserURL)
	}
}
