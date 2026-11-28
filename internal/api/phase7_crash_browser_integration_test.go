package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/X9st/TianFaSRC/internal/agent"
	"github.com/X9st/TianFaSRC/internal/blob"
	"github.com/X9st/TianFaSRC/internal/finding"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/policy"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/tool"
	"github.com/X9st/TianFaSRC/internal/traffic"
	"github.com/chromedp/chromedp"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPhase7CrashChild(t *testing.T) {
	mode := os.Getenv("TIANFA_P7_CRASH_MODE")
	if mode == "" {
		return
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("TIANFA_P7_CRASH_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	taskID := os.Getenv("TIANFA_P7_CRASH_TASK")
	orgID := os.Getenv("TIANFA_P7_CRASH_ORG")
	actorID := os.Getenv("TIANFA_P7_CRASH_ACTOR")
	dataDir := os.Getenv("TIANFA_P7_CRASH_DATA")
	switch mode {
	case "planner":
		claim, ok, err := (agent.Scheduler{Engine: agent.Engine{Pool: pool}}).ClaimPlanner(ctx)
		if err != nil || !ok || claim.TaskID != taskID {
			t.Fatalf("planner claim: %+v %v %v", claim, ok, err)
		}
		fmt.Println("CLAIMED", claim.Fence)
		select {}
	case "tool":
		input, _ := json.Marshal(map[string]any{"url": os.Getenv("TIANFA_P7_CRASH_URL"), "method": "GET"})
		fmt.Println("STARTED")
		_, err := (tool.Dispatcher{Pool: pool, DataDir: dataDir, Enforcer: policy.Enforcer{TestOnlyAllowPrivate: true}}).Execute(ctx, tool.Call{OrganizationID: orgID, TaskID: taskID, IntentID: os.Getenv("TIANFA_P7_CRASH_INTENT"), RunID: os.Getenv("TIANFA_P7_CRASH_RUN"), ActorID: actorID, Name: "http.read", Input: input, IdempotencyKey: os.Getenv("TIANFA_P7_CRASH_KEY")})
		if err != nil {
			t.Fatal(err)
		}
	case "outbox":
		item, err := (finding.Store{Pool: pool}).Create(ctx, orgID, actorID, finding.Input{TaskID: taskID, Title: "崩溃恢复线索", VulnerabilityType: "authorization", Location: "/recover", Severity: "high", AssetIDs: []string{os.Getenv("TIANFA_P7_CRASH_ASSET")}, Impact: "受控数据可读", ReproductionSteps: []string{"GET /recover"}, ObservedResult: "受控响应", Recommendation: "检查权限"})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("COMMITTED", item.ID)
		select {}
	case "blob":
		fmt.Println("STARTED")
		_, err := (traffic.Store{Pool: pool, Blobs: blob.Store{Root: dataDir}}).Record(ctx, traffic.RecordInput{OrganizationID: orgID, TaskID: taskID, Method: "GET", URL: os.Getenv("TIANFA_P7_CRASH_URL"), RemoteIP: "127.0.0.1", StatusCode: 200, RequestRaw: phase7CrashRequest, ResponseRaw: phase7CrashResponse, RequestedAt: time.Now().Add(-time.Second), RespondedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown crash mode %q", mode)
	}
	t.Fatal("crash child unexpectedly returned")
}

var phase7CrashRequest = []byte("GET /blob HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")
var phase7CrashResponse = []byte("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nproof")

func phase7StartCrashChild(t *testing.T, values map[string]string) (*exec.Cmd, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPhase7CrashChild$")
	cmd.Env = os.Environ()
	for key, value := range values {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("crash child did not signal: %v", scanner.Err())
	}
	return cmd, scanner.Text()
}

func phase7StopCrashChild(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
}

func runPhase7CrashSequence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scheduler agent.Scheduler, orgID, actorID, dataDir string, browserCtx context.Context, browserURL string) {
	t.Helper()
	requestSeen := make(chan struct{}, 1)
	releaseResponse := make(chan struct{})
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		requestSeen <- struct{}{}
		<-releaseResponse
		_, _ = io.WriteString(w, "observed once")
	}))
	defer target.Close()
	defer func() {
		select {
		case <-releaseResponse:
		default:
			close(releaseResponse)
		}
	}()
	parsed, _ := url.Parse(target.URL)
	port, _ := strconv.Atoi(parsed.Port())
	recovery, err := (task.Store{Pool: pool}).Create(ctx, orgID, actorID, task.CreateInput{Title: "Phase 7 崩溃恢复验收", Goals: []task.GoalInput{{Text: "验证崩溃后恢复", Priority: 5}}, Scope: struct {
		Entries []task.ScopeEntryInput `json:"entries"`
	}{Entries: []task.ScopeEntryInput{{Effect: "allow", Kind: "ip", Value: "127.0.0.1", Ports: []int{port}}}}})
	if err != nil {
		t.Fatal(err)
	}
	taskID := recovery.Task.ID
	if _, err := pool.Exec(ctx, `UPDATE tasks SET status='running' WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE planner_wakes SET due_at=now()+interval '1 hour' WHERE task_id<>$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO planner_wakes(task_id,due_at,reason) VALUES($1,now()-interval '1 hour','phase7.crash') ON CONFLICT(task_id) DO UPDATE SET pending=true,due_at=excluded.due_at,reason=excluded.reason,expires_at=NULL`, taskID); err != nil {
		t.Fatal(err)
	}
	base, err := url.Parse(os.Getenv("TIANFA_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	base.Path = "/" + pool.Config().ConnConfig.Database
	common := map[string]string{"TIANFA_P7_CRASH_DB": base.String(), "TIANFA_P7_CRASH_TASK": taskID, "TIANFA_P7_CRASH_ORG": orgID, "TIANFA_P7_CRASH_ACTOR": actorID, "TIANFA_P7_CRASH_DATA": dataDir}
	childEnv := func(mode string) map[string]string {
		values := make(map[string]string, len(common)+6)
		for key, value := range common {
			values[key] = value
		}
		values["TIANFA_P7_CRASH_MODE"] = mode
		return values
	}

	// Kill after Planner claim, then prove the old fence cannot settle the lease.
	plannerChild, signal := phase7StartCrashChild(t, childEnv("planner"))
	if !strings.HasPrefix(signal, "CLAIMED ") {
		t.Fatalf("planner crash signal: %q", signal)
	}
	var oldFence int64
	if _, err := fmt.Sscan(strings.TrimPrefix(signal, "CLAIMED "), &oldFence); err != nil {
		t.Fatal(err)
	}
	phase7StopCrashChild(t, plannerChild)
	if _, err := pool.Exec(ctx, `UPDATE planner_wakes SET expires_at=now()-interval '1 second' WHERE task_id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.FinishPlanner(ctx, agent.Claim{TaskID: taskID, OrganizationID: orgID, Fence: oldFence}, nil); !errors.Is(err, task.ErrConflict) {
		t.Fatalf("expired planner fence accepted: %v", err)
	}
	if _, err := (task.Store{Pool: pool}).ProjectActivities(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Do(browserCtx, chromedp.Navigate(browserURL+"/tasks/?id="+taskID), chromedp.WaitVisible(chromedp.CSS(".task-summary"))); err != nil {
		t.Fatal(err)
	}
	for _, visible := range []string{"Phase 7 崩溃恢复验收", "planner.reconciled"} {
		if err := browserWaitText(browserCtx, visible); err != nil {
			t.Fatal(err)
		}
	}

	// Kill after HTTP effect and traffic capture, while the tool settlement row is locked.
	intentID := identity.NewID()
	if _, err := pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'intent','open','崩溃工具验证')`, intentID, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key) VALUES($1,$2,$3,'崩溃工具验证','受控 HTTP 观察',$4)`, intentID, taskID, recovery.Goals[0].ID, "phase7-crash-tool"); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := scheduler.ClaimWorker(ctx)
	if err != nil || !ok || claim.IntentID != intentID {
		t.Fatalf("crash worker claim: %+v %v %v", claim, ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE task_intents SET status='running' WHERE id=$1`, intentID); err != nil {
		t.Fatal(err)
	}
	key := "phase7-crash-tool-" + taskID
	toolEnv := childEnv("tool")
	toolEnv["TIANFA_P7_CRASH_INTENT"] = intentID
	toolEnv["TIANFA_P7_CRASH_RUN"] = claim.RunID
	toolEnv["TIANFA_P7_CRASH_KEY"] = key
	toolEnv["TIANFA_P7_CRASH_URL"] = target.URL + "/effect"
	toolChild, signal := phase7StartCrashChild(t, toolEnv)
	if signal != "STARTED" {
		t.Fatalf("tool crash signal: %q", signal)
	}
	select {
	case <-requestSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("crash tool did not reach target")
	}
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `SELECT 1 FROM task_budgets WHERE task_id=$1 FOR UPDATE`, taskID); err != nil {
		t.Fatal(err)
	}
	close(releaseResponse)
	blocked := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'UPDATE task_budgets SET tool_calls=%')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("tool did not reach writeback lock")
	}
	phase7StopCrashChild(t, toolChild)
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var callID, status string
	if err := pool.QueryRow(ctx, `SELECT id,status FROM tool_calls WHERE idempotency_key=$1`, key).Scan(&callID, &status); err != nil || status != "running" {
		t.Fatalf("tool after kill: %s %s %v", callID, status, err)
	}
	var captured int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM traffic WHERE tool_call_id=$1`, callID).Scan(&captured); err != nil || captured != 1 {
		t.Fatalf("captured traffic=%d %v", captured, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_leases SET expires_at=now()-interval '1 second' WHERE run_id=$1`, claim.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tool_calls SET started_at=now()-interval '31 seconds' WHERE id=$1`, callID); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var errorClass string
	if err := pool.QueryRow(ctx, `SELECT status,error_class FROM tool_calls WHERE id=$1`, callID).Scan(&status, &errorClass); err != nil || status != "failed" || errorClass != "process_interrupted" {
		t.Fatalf("tool recovery: %s %s %v", status, errorClass, err)
	}
	input, _ := json.Marshal(map[string]string{"url": target.URL + "/effect", "method": "GET"})
	replayed, err := (tool.Dispatcher{Pool: pool, DataDir: dataDir, Enforcer: policy.Enforcer{TestOnlyAllowPrivate: true}}).Execute(ctx, tool.Call{OrganizationID: orgID, TaskID: taskID, IntentID: intentID, RunID: claim.RunID, ActorID: actorID, Name: "http.read", Input: input, IdempotencyKey: key})
	if !errors.Is(err, tool.ErrToolBlocked) || replayed.ID != callID || hits.Load() != 1 {
		t.Fatalf("tool replayed effect: %+v %v hits=%d", replayed, err, hits.Load())
	}
	if _, err := (task.Store{Pool: pool}).ProjectActivities(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Do(browserCtx, chromedp.Navigate(browserURL+"/tasks/?id="+taskID), chromedp.WaitVisible(chromedp.CSS(".task-summary"))); err != nil {
		t.Fatal(err)
	}
	if err := browserWaitText(browserCtx, "tool.reconciled"); err != nil {
		t.Fatal(err)
	}

	// Kill after the Finding transaction commits but before Outbox consumption.
	asset, err := (task.Store{Pool: pool}).UpsertAsset(ctx, orgID, actorID, task.AssetInput{Kind: "endpoint", Value: target.URL + "/recover", SourceTaskID: &taskID})
	if err != nil {
		t.Fatal(err)
	}
	outboxEnv := childEnv("outbox")
	outboxEnv["TIANFA_P7_CRASH_ASSET"] = asset.ID
	outboxChild, signal := phase7StartCrashChild(t, outboxEnv)
	if !strings.HasPrefix(signal, "COMMITTED ") {
		t.Fatalf("outbox crash signal: %q", signal)
	}
	findingID := strings.TrimPrefix(signal, "COMMITTED ")
	phase7StopCrashChild(t, outboxChild)
	var eventID string
	if err := pool.QueryRow(ctx, `SELECT id FROM domain_events WHERE task_id=$1 AND event_type='finding.created' AND payload->>'finding_id'=$2`, taskID, findingID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	var unconsumed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_consumptions WHERE event_id=$1`, eventID).Scan(&unconsumed); err != nil || unconsumed != 0 {
		t.Fatalf("outbox consumed before restart: %d %v", unconsumed, err)
	}
	for range 2 {
		if _, err := scheduler.PumpEvents(ctx, 100); err != nil {
			t.Fatal(err)
		}
		if _, err := (task.Store{Pool: pool}).ProjectActivities(ctx, 100); err != nil {
			t.Fatal(err)
		}
	}
	var consumed, activities int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_consumptions WHERE event_id=$1 AND consumer_name='planner'`, eventID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activities WHERE event_id=$1 AND kind='finding.created'`, eventID).Scan(&activities); err != nil || consumed != 1 || activities != 1 {
		t.Fatalf("outbox recovery: consumed=%d activities=%d %v", consumed, activities, err)
	}
	if err := chromedp.Do(browserCtx, chromedp.Navigate(browserURL+"/findings/?task="+taskID+"&tab=findings"), chromedp.WaitVisible(chromedp.CSS(".phase5-tabs"))); err != nil {
		t.Fatal(err)
	}
	if err := browserWaitText(browserCtx, "崩溃恢复线索"); err != nil {
		t.Fatal(err)
	}

	// Kill after Blob bytes are durable but before their database references commit.
	paths := make([]string, 0, 2)
	for _, raw := range [][]byte{phase7CrashRequest, phase7CrashResponse} {
		sum := sha256.Sum256(raw)
		hash := hex.EncodeToString(sum[:])
		paths = append(paths, filepath.Join(dataDir, "blobs", hash[:2], hash))
	}
	blobLock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blobLock.Rollback(ctx)
	if _, err := blobLock.Exec(ctx, `LOCK TABLE blobs IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	blobEnv := childEnv("blob")
	blobEnv["TIANFA_P7_CRASH_URL"] = target.URL + "/blob"
	blobChild, signal := phase7StartCrashChild(t, blobEnv)
	if signal != "STARTED" {
		t.Fatalf("blob crash signal: %q", signal)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		ready := true
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				ready = false
			}
		}
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("blob not durable before kill: %s %v", path, err)
		}
	}
	phase7StopCrashChild(t, blobChild)
	if err := blobLock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var phantom int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM traffic WHERE task_id=$1 AND url=$2`, taskID, target.URL+"/blob").Scan(&phantom); err != nil || phantom != 0 {
		t.Fatalf("uncommitted traffic=%d %v", phantom, err)
	}
	collector := traffic.Store{Pool: pool, Blobs: blob.Store{Root: dataDir}}
	if count, err := collector.Collect(ctx, time.Now().Add(-time.Hour)); err != nil || count != 0 {
		t.Fatalf("early orphan collection: %d %v", count, err)
	}
	if count, err := collector.Collect(ctx, time.Now().Add(time.Hour)); err != nil || count < 2 {
		t.Fatalf("orphan collection: %d %v", count, err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("orphan file remains: %s %v", path, err)
		}
	}
	if err := chromedp.Do(browserCtx, chromedp.Navigate(browserURL+"/tasks/?id="+taskID), chromedp.WaitVisible(chromedp.CSS(".task-summary"))); err != nil {
		t.Fatal(err)
	}
	for _, visible := range []string{"planner.reconciled", "tool.reconciled", "finding.created"} {
		if err := browserWaitText(browserCtx, visible); err != nil {
			t.Fatal(err)
		}
	}
	if err := scheduler.StopIntent(ctx, orgID, actorID, taskID, intentID, "崩溃恢复验证已完成"); err != nil {
		t.Fatal(err)
	}
}
