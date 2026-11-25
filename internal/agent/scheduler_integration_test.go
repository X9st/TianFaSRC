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

	"github.com/X9st/TianFaSRC/internal/auth"
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

func TestBudgetExhaustionSettlesOpenIntent(t *testing.T) {
	s, _, taskID, intentID := schedulerFixture(t)
	ctx := context.Background()
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE tasks SET budget='{"schema_version":1,"max_tool_calls":0}'::jsonb WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimWorker(ctx); err != nil || ok {
		t.Fatalf("claimed over-budget intent: %v %v", ok, err)
	}
	if _, err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT status,blocking_reason FROM task_intents WHERE id=$1`, intentID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "exhausted" || !strings.Contains(reason, "预算") {
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

func TestConcurrentObservationBudgetReservation(t *testing.T) {
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
	if _, err := s.Engine.Pool.Exec(ctx, `UPDATE tasks SET budget='{"schema_version":1,"max_tool_calls":1}'::jsonb WHERE id=$1`, taskID); err != nil {
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
	success, budget := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, errToolBudget) {
			budget++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || budget != 1 || source.calls.Load() != 1 {
		t.Fatalf("success=%d budget=%d calls=%d", success, budget, source.calls.Load())
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
