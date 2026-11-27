package agent

import (
	"context"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BudgetDecision is produced by the Planner and validated by the runtime.
// It is never accepted in a user-facing task mutation.
type BudgetDecision struct {
	ModelRounds int    `json:"model_rounds"`
	ToolCalls   int    `json:"tool_calls"`
	Tokens      int    `json:"tokens"`
	Seconds     int    `json:"seconds"`
	Reason      string `json:"reason"`
}

func (b BudgetDecision) Valid() bool {
	return b.ModelRounds >= 1 && b.ModelRounds <= 8 &&
		b.ToolCalls >= 1 && b.ToolCalls <= 12 &&
		b.Tokens >= 1000 && b.Tokens <= 30000 &&
		b.Seconds >= 15 && b.Seconds <= 180 &&
		len(strings.TrimSpace(b.Reason)) >= 1 && len(b.Reason) <= 500
}

func defaultBudget(priority, attempt int) BudgetDecision {
	// The Planner can override this conservative fallback. A retry receives
	// less room unless the Planner explicitly finds a reason to extend it.
	decision := BudgetDecision{ModelRounds: 4, ToolCalls: 4, Tokens: 12000, Seconds: 90, Reason: "系统保守默认值"}
	if priority >= 8 && attempt <= 1 {
		decision = BudgetDecision{ModelRounds: 5, ToolCalls: 6, Tokens: 16000, Seconds: 120, Reason: "高优先级方向的系统默认值"}
	}
	if attempt >= 2 {
		decision = BudgetDecision{ModelRounds: 3, ToolCalls: 3, Tokens: 9000, Seconds: 75, Reason: "重复探索时收紧的系统默认值"}
	}
	return decision
}

type WorkerBudget struct {
	RunID            string    `json:"run_id"`
	IntentID         string    `json:"intent_id"`
	TaskID           string    `json:"task_id"`
	MaxModelRounds   int       `json:"max_model_rounds"`
	MaxToolCalls     int       `json:"max_tool_calls"`
	MaxTokens        int       `json:"max_tokens"`
	MaxSeconds       int       `json:"max_seconds"`
	ModelRoundsUsed  int       `json:"model_rounds_used"`
	ToolCallsUsed    int       `json:"tool_calls_used"`
	InputTokensUsed  int64     `json:"input_tokens_used"`
	OutputTokensUsed int64     `json:"output_tokens_used"`
	Reason           string    `json:"reason"`
	StartedAt        time.Time `json:"started_at"`
}

func GetWorkerBudget(ctx context.Context, pool *pgxpool.Pool, orgID, runID string) (WorkerBudget, error) {
	var item WorkerBudget
	err := pool.QueryRow(ctx, `SELECT run_id,intent_id,task_id,max_model_rounds,max_tool_calls,max_tokens,max_seconds,model_rounds_used,tool_calls_used,input_tokens_used,output_tokens_used,reason,started_at FROM worker_run_budgets WHERE run_id=$1 AND organization_id=$2`, runID, orgID).Scan(&item.RunID, &item.IntentID, &item.TaskID, &item.MaxModelRounds, &item.MaxToolCalls, &item.MaxTokens, &item.MaxSeconds, &item.ModelRoundsUsed, &item.ToolCallsUsed, &item.InputTokensUsed, &item.OutputTokensUsed, &item.Reason, &item.StartedAt)
	if err == pgx.ErrNoRows {
		return WorkerBudget{}, task.ErrNotFound
	}
	return item, err
}

func ListWorkerBudgets(ctx context.Context, pool *pgxpool.Pool, orgID, taskID string) ([]WorkerBudget, error) {
	rows, err := pool.Query(ctx, `SELECT b.run_id FROM worker_run_budgets b JOIN tasks t ON t.id=b.task_id WHERE t.id=$1 AND t.organization_id=$2 ORDER BY b.started_at DESC,b.run_id DESC LIMIT 200`, taskID, orgID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	items := make([]WorkerBudget, 0, len(ids))
	for _, id := range ids {
		item, err := GetWorkerBudget(ctx, pool, orgID, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
