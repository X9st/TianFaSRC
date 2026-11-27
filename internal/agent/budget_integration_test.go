package agent

import (
	"context"
	"testing"

	"github.com/X9st/TianFaSRC/internal/identity"
)

func TestWorkerBudgetsAreIndependentSnapshots(t *testing.T) {
	s, orgID, taskID, firstIntent := schedulerFixture(t)
	ctx := context.Background()
	_, err := s.Engine.Pool.Exec(ctx, `UPDATE task_intents SET budget_model_rounds=2,budget_tool_calls=1,budget_tokens=3000,budget_seconds=30,budget_reason='收敛验证' WHERE id=$1`, firstIntent)
	if err != nil {
		t.Fatal(err)
	}
	var goalID string
	if err := s.Engine.Pool.QueryRow(ctx, `SELECT goal_id FROM task_intents WHERE id=$1`, firstIntent).Scan(&goalID); err != nil {
		t.Fatal(err)
	}
	secondIntent := identity.NewID()
	_, err = s.Engine.Pool.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'intent','open','独立方向')`, secondIntent, taskID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Engine.Pool.Exec(ctx, `INSERT INTO task_intents(id,task_id,goal_id,objective,success_criteria,dedup_key,budget_model_rounds,budget_tool_calls,budget_tokens,budget_seconds,budget_reason) VALUES($1,$2,$3,'独立方向','响应','independent',5,6,16000,120,'扩大探索')`, secondIntent, taskID, goalID)
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("first claim: %v, %v", ok, err)
	}
	second, ok, err := s.ClaimWorker(ctx)
	if err != nil || !ok {
		t.Fatalf("second claim: %v, %v", ok, err)
	}
	budgets, err := ListWorkerBudgets(ctx, s.Engine.Pool, orgID, taskID)
	if err != nil || len(budgets) != 2 {
		t.Fatalf("budgets: %v, %v", budgets, err)
	}
	firstBudget, err := GetWorkerBudget(ctx, s.Engine.Pool, orgID, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	secondBudget, err := GetWorkerBudget(ctx, s.Engine.Pool, orgID, second.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if firstBudget.MaxToolCalls != 1 || secondBudget.MaxToolCalls != 6 {
		t.Fatalf("budgets mixed: %+v %+v", firstBudget, secondBudget)
	}
	_, err = s.Engine.Pool.Exec(ctx, `UPDATE worker_run_budgets SET tool_calls_used=1 WHERE run_id=$1`, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	secondBudget, err = GetWorkerBudget(ctx, s.Engine.Pool, orgID, second.RunID)
	if err != nil || secondBudget.ToolCallsUsed != 0 {
		t.Fatalf("usage leaked: %+v %v", secondBudget, err)
	}
}
