package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/X9st/TianFaSRC/internal/auth"
	"github.com/X9st/TianFaSRC/internal/finding"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/X9st/TianFaSRC/internal/store/postgres"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type countingObservation struct{ calls atomic.Int32 }

func (o *countingObservation) ObserveOnce(_ context.Context, _, _, _ string) (Observation, error) {
	o.calls.Add(1)
	return Observation{Summary: "受控响应", ToolCallID: identity.NewID(), Confirmed: true}, nil
}

func schedulerFixture(t *testing.T) (Scheduler, string, string, string) {
	t.Helper()
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
	dbName := "tianfa_sched_" + strings.ReplaceAll(identity.NewID(), "-", "")[16:]
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+dbName); _ = admin.Close(ctx) })
	config, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = dbName
	config.MaxConns = 30
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	user, err := (auth.Store{Pool: pool}).InitAdmin(ctx, "admin@example.invalid", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	profiles := llm.Store{Pool: pool}
	if _, err = profiles.ActivePrompt(ctx, user.OrganizationID, user.ID, "worker"); err != nil {
		t.Fatal(err)
	}
	item, err := (task.Store{Pool: pool}).Create(ctx, user.OrganizationID, user.ID, task.CreateInput{Title: "调度验收", Description: "授权测试", Goals: []task.GoalInput{{Text: "验证结果", Priority: 5}}, Scope: struct {
		Entries []task.ScopeEntryInput `json:"entries"`
	}{Entries: []task.ScopeEntryInput{{Effect: "allow", Kind: "domain", Value: "example.invalid"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE tasks SET status='running' WHERE id=$1`, item.Task.ID); err != nil {
		t.Fatal(err)
	}
	intentID := identity.NewID()
	if _, err = pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'intent','open','验证首页')`, intentID, item.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key) VALUES($1,$2,$3,'验证首页','受控响应','test')`, intentID, item.Task.ID, item.Goals[0].ID); err != nil {
		t.Fatal(err)
	}
	return Scheduler{Engine: Engine{Pool: pool, Profiles: profiles}}, user.OrganizationID, item.Task.ID, intentID
}

func TestConcurrentClaimsRecoveryAndFencing(t *testing.T) {
	s, orgID, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	claims := make(chan Claim, 20)
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, ok, err := s.ClaimWorker(ctx)
			if err != nil {
				errs <- err
			} else if ok {
				claims <- c
			}
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("claims=%d, want 1", len(claims))
	}
	first := <-claims
	if first.IntentID != intentID || first.Attempt != 1 {
		t.Fatalf("first claim=%+v", first)
	}
	if ok, err := s.Renew(ctx, first); err != nil || !ok {
		t.Fatalf("renew=%v %v", ok, err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE worker_leases SET expires_at=now()-interval '1 second' WHERE intent_id=$1`, intentID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Reconcile(ctx); err != nil || n != 1 {
		t.Fatalf("reconcile=%d %v", n, err)
	}
	second, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("second claim=%v %v", ok, err)
	}
	if second.Attempt != 2 || second.RunID == first.RunID {
		t.Fatalf("second claim=%+v", second)
	}
	obs := Observation{Summary: "受控响应 200", ToolCallID: identity.NewID(), Confirmed: true}
	if err := s.FinishWorker(ctx, first, "done", "", &obs); !errors.Is(err, task.ErrConflict) {
		t.Fatalf("stale finish=%v", err)
	}
	if err := s.FinishWorker(ctx, second, "done", "", &obs); err != nil {
		t.Fatal(err)
	}
	var facts, runs, leases int
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM exploration_nodes WHERE task_id=$1 AND kind='fact'`, taskID).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE task_id=$1 AND role='worker'`, taskID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM worker_leases WHERE task_id=$1`, taskID).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if facts != 1 || runs != 2 || leases != 0 {
		t.Fatalf("facts=%d runs=%d leases=%d", facts, runs, leases)
	}
	intents, err := s.Engine.ListIntents(ctx, orgID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].Status != "done" {
		t.Fatalf("intents=%+v", intents)
	}
}

func TestPauseCancelAndReconcileRaceHasOneTerminalReason(t *testing.T) {
	s, orgID, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	var actorID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT created_by FROM tasks WHERE id=$1`, taskID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimWorker(ctx); err != nil || !ok {
		t.Fatalf("claim before control race: %v %v", ok, err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE worker_leases SET expires_at=now()-interval '1 second' WHERE intent_id=$1`, intentID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 3)
	var wg sync.WaitGroup
	for _, operation := range []func() error{
		func() error { return s.TaskCommand(ctx, orgID, actorID, taskID, "pause") },
		func() error { return s.TaskCommand(ctx, orgID, actorID, taskID, "cancel") },
		func() error { _, err := s.Reconcile(ctx); return err },
	} {
		wg.Add(1)
		go func(operation func() error) { defer wg.Done(); <-start; results <- operation() }(operation)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, task.ErrConflict) {
			t.Fatal(err)
		}
	}
	if _, err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var status, intentStatus string
	var stopReason, blockingReason *string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT t.status,i.status,i.stop_reason,i.blocking_reason FROM tasks t JOIN task_intents i ON i.task_id=t.id WHERE t.id=$1 AND i.id=$2`, taskID, intentID).Scan(&status, &intentStatus, &stopReason, &blockingReason); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || intentStatus != "stopped" {
		t.Fatalf("task=%s intent=%s stop_reason=%v blocking_reason=%v", status, intentStatus, stopReason, blockingReason)
	}
	if (stopReason == nil || *stopReason != "任务已取消") && (blockingReason == nil || *blockingReason != "任务已取消") {
		t.Fatalf("terminal reason missing: stop=%v blocking=%v", stopReason, blockingReason)
	}
}

func TestTransientDatabaseOutageDoesNotCompleteTask(t *testing.T) {
	s, _, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	originalPool := s.Engine.Pool
	claim, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("claim before outage: %v %v", ok, err)
	}
	var unavailable atomic.Bool
	config := originalPool.Config()
	config.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		if unavailable.Load() {
			return errors.New("injected PostgreSQL outage")
		}
		return nil
	}
	outagePool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(outagePool.Close)
	if err := outagePool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	s.Engine.Pool = outagePool
	unavailable.Store(true)
	outagePool.Reset()
	if err := s.FinishWorker(ctx, claim, "done", "", &Observation{Summary: "测试观察", ToolCallID: identity.NewID(), Confirmed: true}); err == nil {
		t.Fatal("worker finish succeeded during database outage")
	}
	if _, err := s.Reconcile(ctx); err == nil {
		t.Fatal("reconciliation reported success during database outage")
	}
	var taskStatus string
	if err := originalPool.QueryRow(ctx, `SELECT status FROM tasks WHERE id=$1`, taskID).Scan(&taskStatus); err != nil || taskStatus != "running" {
		t.Fatalf("task status during outage=%s err=%v", taskStatus, err)
	}
	unavailable.Store(false)
	if _, err := outagePool.Exec(ctx, `UPDATE worker_leases SET expires_at=now()-interval '1 second' WHERE intent_id=$1`, intentID); err != nil {
		t.Fatal(err)
	}
	if count, err := s.Reconcile(ctx); err != nil || count != 1 {
		t.Fatalf("recovery reconciliation=%d %v", count, err)
	}
	reclaimed, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok || reclaimed.Attempt != 2 {
		t.Fatalf("reclaimed after outage=%+v %v %v", reclaimed, ok, err)
	}
	if err := originalPool.QueryRow(ctx, `SELECT status FROM tasks WHERE id=$1`, taskID).Scan(&taskStatus); err != nil || taskStatus != "running" {
		t.Fatalf("task status after recovery=%s err=%v", taskStatus, err)
	}
}

func TestPauseResumeAndMaxAttempts(t *testing.T) {
	s, orgID, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	var actorID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT created_by FROM tasks WHERE id=$1`, taskID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err := s.TaskCommand(ctx, orgID, actorID, taskID, "pause"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimWorker(ctx); err != nil || ok {
		t.Fatalf("claimed paused task: %v %v", ok, err)
	}
	if err := s.TaskCommand(ctx, orgID, actorID, taskID, "resume"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE tasks SET status='running' WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		claim, ok, err := s.ClaimWorker(ctx)
		if err != nil || !ok {
			t.Fatalf("attempt %d: %v %v", attempt, ok, err)
		}
		if _, err = s.Engine.Pool.Exec(ctx, `UPDATE worker_leases SET expires_at=now()-interval '1 second' WHERE intent_id=$1`, intentID); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if attempt == 3 {
			var status, reason string
			if err = s.Engine.Pool.QueryRow(ctx, `SELECT status,blocking_reason FROM task_intents WHERE id=$1`, intentID).Scan(&status, &reason); err != nil {
				t.Fatal(err)
			}
			if status != "blocked" || !strings.Contains(reason, "最大重试") {
				t.Fatalf("%s %s", status, reason)
			}
		}
		_ = claim
	}
}

func TestObservationCheckpointAndExplicitRetryGeneration(t *testing.T) {
	s, orgID, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	source := &countingObservation{}
	s.Observations = source
	first, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	initial, err := s.observe(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Engine.Pool.Exec(ctx, `UPDATE worker_leases SET expires_at=now()-interval '1 second' WHERE intent_id=$1`, intentID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	second, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("reclaim: %v %v", ok, err)
	}
	again, err := s.observe(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if source.calls.Load() != 1 || initial.ToolCallID != again.ToolCallID {
		t.Fatalf("duplicate observation: calls=%d", source.calls.Load())
	}
	if err = s.FinishWorker(ctx, second, "blocked", "外部条件", nil); err != nil {
		t.Fatal(err)
	}
	var actorID string
	if err = s.Engine.Pool.QueryRow(ctx, `SELECT created_by FROM tasks WHERE id=$1`, taskID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err = s.RetryIntent(ctx, orgID, actorID, taskID, intentID); err != nil {
		t.Fatal(err)
	}
	third, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("rerun: %v %v", ok, err)
	}
	if third.ObservationGeneration != 2 {
		t.Fatalf("generation=%d", third.ObservationGeneration)
	}
	if _, err = s.observe(ctx, third); err != nil {
		t.Fatal(err)
	}
	if source.calls.Load() != 2 {
		t.Fatalf("calls=%d", source.calls.Load())
	}
	var toolCalls int
	if err = s.Engine.Pool.QueryRow(ctx, `SELECT tool_calls FROM task_budgets WHERE task_id=$1`, taskID).Scan(&toolCalls); err != nil {
		t.Fatal(err)
	}
	if toolCalls != 2 {
		t.Fatalf("tool calls=%d", toolCalls)
	}
}

func TestPlannerTodoDependencyDispatch(t *testing.T) {
	s, orgID, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	if _, err := s.Engine.Pool.Exec(ctx, `DELETE FROM task_intents WHERE task_id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `DELETE FROM exploration_nodes WHERE task_id=$1 AND kind='intent'`, taskID); err != nil {
		t.Fatal(err)
	}
	var goalID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT id FROM task_goals WHERE task_id=$1`, taskID).Scan(&goalID); err != nil {
		t.Fatal(err)
	}
	plan := `{"todos":[{"key":"root","title":"先验证入口","depends_on":[]},{"key":"child","title":"再验证下游","depends_on":["root"]}],"intents":[{"goal_id":"` + goalID + `","objective":"验证入口事实","success_criteria":"受控结果","todo_key":"root"},{"goal_id":"` + goalID + `","objective":"验证下游事实","success_criteria":"受控结果","todo_key":"child"}],"planning_blocked":""}`
	first, err := s.Engine.insertIntents(ctx, orgID, taskID, plan, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("first intents=%d", len(first))
	}
	claim, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("claim=%v %v", ok, err)
	}
	if claim.IntentID != first[0].ID {
		t.Fatalf("wrong intent %s", claim.IntentID)
	}
	if err = s.FinishWorker(ctx, claim, "done", "", &Observation{Summary: "入口确认", ToolCallID: identity.NewID(), Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	second, err := s.Engine.insertIntents(ctx, orgID, taskID, plan, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Objective != "验证下游事实" {
		t.Fatalf("second intents=%+v", second)
	}
	todos, err := s.ListTodos(ctx, orgID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]Todo{}
	for _, item := range todos {
		states[item.Title] = item
	}
	if len(todos) != 2 || states["先验证入口"].Status != "done" || len(states["再验证下游"].DependsOn) != 1 {
		t.Fatalf("todos=%+v", todos)
	}
}

func TestWorkerPriorityAndTaskFairness(t *testing.T) {
	s, orgID, firstTask, firstIntent := schedulerFixture(t)
	ctx := context.Background()
	var actorID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT created_by FROM tasks WHERE id=$1`, firstTask).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	secondTask, err := (task.Store{Pool: s.Engine.Pool}).Create(ctx, orgID, actorID, task.CreateInput{Title: "第二任务", Description: "授权测试", Goals: []task.GoalInput{{Text: "验证第二目标", Priority: 5}}, Scope: struct {
		Entries []task.ScopeEntryInput `json:"entries"`
	}{Entries: []task.ScopeEntryInput{{Effect: "allow", Kind: "domain", Value: "second.invalid"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Engine.Pool.Exec(ctx, `UPDATE tasks SET status='running' WHERE id=$1`, secondTask.Task.ID); err != nil {
		t.Fatal(err)
	}
	add := func(taskID, goalID, summary string, priority int) string {
		id := identity.NewID()
		if _, err = s.Engine.Pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'intent','open',$3)`, id, taskID, summary); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Engine.Pool.Exec(ctx, `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key,priority) VALUES($1,$2,$3,$4,'受控结果',$5,$6)`, id, taskID, goalID, summary, summary, priority); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if _, err = s.Engine.Pool.Exec(ctx, `UPDATE task_intents SET priority=1 WHERE id=$1`, firstIntent); err != nil {
		t.Fatal(err)
	}
	var firstGoal string
	if err = s.Engine.Pool.QueryRow(ctx, `SELECT id FROM task_goals WHERE task_id=$1`, firstTask).Scan(&firstGoal); err != nil {
		t.Fatal(err)
	}
	high := add(firstTask, firstGoal, "高优先级", 9)
	other := add(secondTask.Task.ID, secondTask.Goals[0].ID, "第二任务方向", 5)
	a, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("first: %v %v", ok, err)
	}
	b, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("second: %v %v", ok, err)
	}
	if a.IntentID != high || b.IntentID != other {
		t.Fatalf("priority/fairness: first=%s second=%s", a.IntentID, b.IntentID)
	}
}

func TestDeadlineSettlesOpenIntent(t *testing.T) {
	s, _, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE tasks SET deadline_at=now()-interval '1 second' WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimWorker(ctx); err != nil || ok {
		t.Fatalf("claimed past-deadline intent: %v %v", ok, err)
	}
	if _, err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT status,blocking_reason FROM task_intents WHERE id=$1`, intentID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "exhausted" || !strings.Contains(reason, "截止时间") {
		t.Fatalf("status=%s reason=%s", status, reason)
	}
}

func TestStoppingBlockedIntentShowsNewReason(t *testing.T) {
	s, orgID, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	var actorID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT created_by FROM tasks WHERE id=$1`, taskID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE task_intents SET status='blocked',blocking_reason='旧阻塞原因' WHERE id=$1`, intentID); err != nil {
		t.Fatal(err)
	}
	if err := s.StopIntent(ctx, orgID, actorID, taskID, intentID, "人工停止原因"); err != nil {
		t.Fatal(err)
	}
	intents, err := s.Engine.ListIntents(ctx, orgID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].Status != "stopped" || intents[0].BlockingReason != nil || intents[0].StopReason == nil || *intents[0].StopReason != "人工停止原因" {
		t.Fatalf("intent=%+v", intents)
	}
}

func TestCompletedTaskNewGoalRequeuesPlanner(t *testing.T) {
	s, orgID, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	var actorID string
	var version int64
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT created_by,version FROM tasks WHERE id=$1`, taskID).Scan(&actorID, &version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE tasks SET status='completed' WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	item, err := (task.Store{Pool: s.Engine.Pool}).AddGoal(ctx, orgID, actorID, taskID, version, task.GoalInput{Text: "新增验证目标", Priority: 5})
	if err != nil {
		t.Fatal(err)
	}
	if item.Task.Status != "queued" || len(item.Goals) != 2 {
		t.Fatalf("task=%+v goals=%d", item.Task, len(item.Goals))
	}
	var pending bool
	if err = s.Engine.Pool.QueryRow(ctx, `SELECT pending FROM planner_wakes WHERE task_id=$1`, taskID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("planner wake missing")
	}
}

func TestPlannerWakeCursorAndReclaim(t *testing.T) {
	s, orgID, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	if _, err := s.Engine.Pool.Exec(ctx, `INSERT INTO planner_wakes(task_id,due_at,reason) VALUES($1,now(),'test')`, taskID); err != nil {
		t.Fatal(err)
	}
	first, ok, err := s.ClaimPlanner(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim=%v %v", ok, err)
	}
	if first.PlannerSince != nil {
		t.Fatalf("first cursor=%v", first.PlannerSince)
	}
	if err = s.FinishPlanner(ctx, first, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Wake(ctx, orgID, taskID, "human.hint"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Engine.Pool.Exec(ctx, `UPDATE planner_wakes SET due_at=now() WHERE task_id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	second, ok, err := s.ClaimPlanner(ctx)
	if err != nil || !ok {
		t.Fatalf("second claim=%v %v", ok, err)
	}
	if second.PlannerSince == nil || second.Fence <= first.Fence {
		t.Fatalf("second=%+v", second)
	}
}

func TestSchedulerCrashHelper(t *testing.T) {
	dsn := os.Getenv("TIANFA_SCHED_CRASH_DB")
	if dsn == "" {
		return
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	c, ok, err := (Scheduler{Engine: Engine{Pool: pool}}).ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("claim=%v %v", ok, err)
	}
	fmt.Println("CLAIMED", c.IntentID, c.RunID)
	select {}
}

func TestPlannerCrashHelper(t *testing.T) {
	dsn := os.Getenv("TIANFA_PLANNER_CRASH_DB")
	if dsn == "" {
		return
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	claim, ok, err := (Scheduler{Engine: Engine{Pool: pool}}).ClaimPlanner(ctx)
	if err != nil || !ok {
		t.Fatalf("claim planner: %v %v", ok, err)
	}
	fmt.Println("CLAIMED", claim.TaskID, claim.Fence)
	select {}
}

func TestKilledPlannerLeaseIsFencedAndReported(t *testing.T) {
	s, orgID, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	if _, err := s.Engine.Pool.Exec(ctx, `INSERT INTO planner_wakes(task_id,due_at,reason) VALUES($1,now(),'test.planner_crash')`, taskID); err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(os.Getenv("TIANFA_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + s.Engine.Pool.Config().ConnConfig.Database
	cmd := exec.Command(os.Args[0], "-test.run=^TestPlannerCrashHelper$")
	cmd.Env = append(os.Environ(), "TIANFA_PLANNER_CRASH_DB="+dsn.String())
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "CLAIMED ") {
		_ = cmd.Process.Kill()
		t.Fatalf("planner helper did not claim: %q (%v)", scanner.Text(), scanner.Err())
	}
	claimed := strings.Fields(scanner.Text())
	if len(claimed) != 3 || claimed[1] != taskID {
		_ = cmd.Process.Kill()
		t.Fatalf("unexpected planner claim: %q", scanner.Text())
	}
	var oldFence int64
	if _, err := fmt.Sscan(claimed[2], &oldFence); err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE planner_wakes SET expires_at=now()-interval '1 second' WHERE task_id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	oldClaim := Claim{TaskID: taskID, OrganizationID: orgID, Fence: oldFence}
	if err := s.FinishPlanner(ctx, oldClaim, nil); !errors.Is(err, task.ErrConflict) {
		t.Fatalf("expired planner was not fenced: %v", err)
	}
	var pending bool
	var reason string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT pending,reason FROM planner_wakes WHERE task_id=$1`, taskID).Scan(&pending, &reason); err != nil || !pending || reason != "lease_expired" {
		t.Fatalf("planner wake after crash: pending=%v reason=%q err=%v", pending, reason, err)
	}
	if _, err := (task.Store{Pool: s.Engine.Pool}).ProjectActivities(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var recoveryActivities int
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM activities WHERE task_id=$1 AND kind='planner.reconciled'`, taskID).Scan(&recoveryActivities); err != nil || recoveryActivities != 1 {
		t.Fatalf("planner recovery activity=%d err=%v", recoveryActivities, err)
	}
	newClaim, ok, err := s.ClaimPlanner(ctx)
	if err != nil || !ok || newClaim.TaskID != taskID || newClaim.Fence <= oldClaim.Fence {
		t.Fatalf("planner was not reclaimed: %+v %v %v", newClaim, ok, err)
	}
}

func TestOutboxCrashHelper(t *testing.T) {
	dsn := os.Getenv("TIANFA_OUTBOX_CRASH_DB")
	taskID := os.Getenv("TIANFA_OUTBOX_CRASH_TASK")
	assetID := os.Getenv("TIANFA_OUTBOX_CRASH_ASSET")
	if dsn == "" || taskID == "" || assetID == "" {
		return
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var orgID, actorID string
	if err := pool.QueryRow(ctx, `SELECT organization_id,created_by FROM tasks WHERE id=$1`, taskID).Scan(&orgID, &actorID); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("TIANFA_FINDING_CRASH_BEFORE_COMMIT") == "1" {
		fmt.Println("STARTED")
	}
	item, err := (finding.Store{Pool: pool}).Create(ctx, orgID, actorID, finding.Input{TaskID: taskID, Title: "崩溃边界线索", VulnerabilityType: "authorization", Location: "/fixture", Severity: "high", AssetIDs: []string{assetID}, Impact: "受控数据可读", ReproductionSteps: []string{"GET /fixture"}, ObservedResult: "受控响应", Recommendation: "检查权限"})
	if err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := pool.QueryRow(ctx, `SELECT id FROM domain_events WHERE task_id=$1 AND event_type='finding.created' AND payload->>'finding_id'=$2`, taskID, item.ID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	fmt.Println("COMMITTED", item.ID, eventID)
	select {}
}

func TestFindingOutboxSurvivesProcessKill(t *testing.T) {
	s, orgID, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	var actorID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT created_by FROM tasks WHERE id=$1`, taskID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	asset, err := (task.Store{Pool: s.Engine.Pool}).UpsertAsset(ctx, orgID, actorID, task.AssetInput{Kind: "endpoint", Value: "https://example.invalid/fixture", SourceTaskID: &taskID})
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(os.Getenv("TIANFA_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + s.Engine.Pool.Config().ConnConfig.Database
	cmd := exec.Command(os.Args[0], "-test.run=^TestOutboxCrashHelper$")
	cmd.Env = append(os.Environ(), "TIANFA_OUTBOX_CRASH_DB="+dsn.String(), "TIANFA_OUTBOX_CRASH_TASK="+taskID, "TIANFA_OUTBOX_CRASH_ASSET="+asset.ID)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "COMMITTED ") {
		_ = cmd.Process.Kill()
		t.Fatalf("helper did not commit an event: %q (%v)", scanner.Text(), scanner.Err())
	}
	committed := strings.Fields(scanner.Text())
	if len(committed) != 3 {
		_ = cmd.Process.Kill()
		t.Fatalf("unexpected helper output: %q", scanner.Text())
	}
	findingID, eventID := committed[1], committed[2]
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	var unconsumed int
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM event_consumptions WHERE event_id=$1`, eventID).Scan(&unconsumed); err != nil || unconsumed != 0 {
		t.Fatalf("event consumed before restart: %d %v", unconsumed, err)
	}
	restarted := Scheduler{Engine: Engine{Pool: s.Engine.Pool}, OwnerID: "restarted-scheduler"}
	activities := task.Store{Pool: s.Engine.Pool}
	for range 2 {
		if _, err := restarted.PumpEvents(ctx, 100); err != nil {
			t.Fatal(err)
		}
		if _, err := activities.ProjectActivities(ctx, 100); err != nil {
			t.Fatal(err)
		}
	}
	var eventCount, plannerCount, activityCount int
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE id=$1 AND task_id=$2 AND organization_id=$3`, eventID, taskID, orgID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	var findingCount, auditCount int
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM findings f JOIN exploration_nodes n ON n.id=f.node_id WHERE f.id=$1 AND f.task_id=$2 AND n.kind='finding'`, findingID, taskID).Scan(&findingCount); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='finding.created' AND resource_id=$1`, findingID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM event_consumptions WHERE event_id=$1 AND consumer_name='planner'`, eventID).Scan(&plannerCount); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM activities WHERE event_id=$1 AND task_id=$2 AND kind='finding.created'`, eventID, taskID).Scan(&activityCount); err != nil {
		t.Fatal(err)
	}
	var pending bool
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT pending FROM planner_wakes WHERE task_id=$1`, taskID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if findingCount != 1 || auditCount != 1 || eventCount != 1 || plannerCount != 1 || activityCount != 1 || !pending {
		t.Fatalf("recovered finding=%d audit=%d event=%d planner=%d activity=%d pending=%v", findingCount, auditCount, eventCount, plannerCount, activityCount, pending)
	}
}

func TestFindingWriteKillRollsBackGraphAndOutbox(t *testing.T) {
	s, orgID, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	var actorID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT created_by FROM tasks WHERE id=$1`, taskID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	asset, err := (task.Store{Pool: s.Engine.Pool}).UpsertAsset(ctx, orgID, actorID, task.AssetInput{Kind: "endpoint", Value: "https://example.invalid/fixture", SourceTaskID: &taskID})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `LOCK TABLE exploration_edges IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(os.Getenv("TIANFA_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + s.Engine.Pool.Config().ConnConfig.Database
	cmd := exec.Command(os.Args[0], "-test.run=^TestOutboxCrashHelper$")
	cmd.Env = append(os.Environ(), "TIANFA_OUTBOX_CRASH_DB="+dsn.String(), "TIANFA_OUTBOX_CRASH_TASK="+taskID, "TIANFA_OUTBOX_CRASH_ASSET="+asset.ID, "TIANFA_FINDING_CRASH_BEFORE_COMMIT=1")
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "STARTED" {
		_ = cmd.Process.Kill()
		t.Fatalf("finding helper did not start: %q (%v)", scanner.Text(), scanner.Err())
	}
	blocked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := s.Engine.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND query LIKE 'INSERT INTO exploration_edges%' AND wait_event_type='Lock')`).Scan(&blocked); err != nil {
			_ = cmd.Process.Kill()
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		_ = cmd.Process.Kill()
		t.Fatal("finding write did not reach the graph edge before kill")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var nodes, findings, events, audits int
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM exploration_nodes WHERE task_id=$1 AND kind='finding'`, taskID).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM findings WHERE task_id=$1`, taskID).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE task_id=$1 AND event_type='finding.created'`, taskID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='finding.created' AND organization_id=$1`, orgID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if nodes != 0 || findings != 0 || events != 0 || audits != 0 {
		t.Fatalf("partial finding commit after kill: nodes=%d findings=%d events=%d audits=%d", nodes, findings, events, audits)
	}
}

func TestKilledWorkerProcessRecovers(t *testing.T) {
	s, _, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSchedulerCrashHelper$")
	dsn, err := url.Parse(os.Getenv("TIANFA_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + s.Engine.Pool.Config().ConnConfig.Database
	cmd.Env = append(os.Environ(), "TIANFA_SCHED_CRASH_DB="+dsn.String())
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		_ = cmd.Process.Kill()
		t.Fatalf("helper never claimed: %v", scanner.Err())
	}
	if !strings.HasPrefix(scanner.Text(), "CLAIMED ") {
		_ = cmd.Process.Kill()
		lines := []string{scanner.Text()}
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		t.Fatalf("helper output: %s", strings.Join(lines, " | "))
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if _, err = s.Engine.Pool.Exec(ctx, `UPDATE worker_leases SET expires_at=now()-interval '1 second' WHERE intent_id=$1`, intentID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Reconcile(ctx); err != nil || n != 1 {
		t.Fatalf("reconcile=%d %v", n, err)
	}
	newClaim, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("reclaim=%v %v", ok, err)
	}
	if newClaim.Attempt != 2 || newClaim.IntentID != intentID {
		t.Fatalf("new claim=%+v", newClaim)
	}
	var oldRuns int
	if err = s.Engine.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE task_id=$1 AND role='worker' AND status='interrupted'`, taskID).Scan(&oldRuns); err != nil {
		t.Fatal(err)
	}
	if oldRuns != 1 {
		t.Fatalf("interrupted runs=%d", oldRuns)
	}
}

func TestConcurrentObservationRecording(t *testing.T) {
	s, _, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	var goalID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT id FROM task_goals WHERE task_id=$1`, taskID).Scan(&goalID); err != nil {
		t.Fatal(err)
	}
	id := identity.NewID()
	if _, err := s.Engine.Pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'intent','open','第二方向')`, id, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key) VALUES($1,$2,$3,'第二方向','受控结果','second')`, id, taskID, goalID); err != nil {
		t.Fatal(err)
	}
	a, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("a=%v %v", ok, err)
	}
	b, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("b=%v %v", ok, err)
	}
	source := &countingObservation{}
	s.Observations = source
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, claim := range []Claim{a, b} {
		wg.Add(1)
		go func(c Claim) { defer wg.Done(); _, err := s.observe(ctx, c); results <- err }(claim)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			t.Fatal(err)
		}
	}
	if success != 2 || source.calls.Load() != 2 {
		t.Fatalf("success=%d calls=%d", success, source.calls.Load())
	}
}

func TestGlobalWorkerCapacity(t *testing.T) {
	s, _, taskID, _ := schedulerFixture(t)
	ctx := context.Background()
	var goalID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT id FROM task_goals WHERE task_id=$1`, taskID).Scan(&goalID); err != nil {
		t.Fatal(err)
	}
	id := identity.NewID()
	if _, err := s.Engine.Pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'intent','open','第二方向')`, id, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key) VALUES($1,$2,$3,'第二方向','受控结果','second')`, id, taskID, goalID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE scheduler_capacity SET max_workers=1 WHERE singleton=true`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan Claim, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, ok, err := s.ClaimWorker(ctx)
			if err != nil {
				errs <- err
			} else if ok {
				claims <- c
			}
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("claims=%d", len(claims))
	}
}
