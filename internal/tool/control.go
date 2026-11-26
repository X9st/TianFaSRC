package tool

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
)

type CallRecord struct {
	ID           string          `json:"id"`
	TaskID       string          `json:"task_id"`
	IntentID     string          `json:"intent_id"`
	RunID        string          `json:"run_id"`
	Name         string          `json:"name"`
	Input        json.RawMessage `json:"input"`
	Status       string          `json:"status"`
	PolicyReason *string         `json:"policy_reason,omitempty"`
	Summary      *string         `json:"summary,omitempty"`
	Output       json.RawMessage `json:"output,omitempty"`
	ScopeVersion int64           `json:"scope_version"`
	CreatedAt    time.Time       `json:"created_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
}

type Approval struct {
	ID         string     `json:"id"`
	ToolCallID string     `json:"tool_call_id"`
	TaskID     string     `json:"task_id"`
	ToolName   string     `json:"tool_name"`
	Status     string     `json:"status"`
	Reason     *string    `json:"reason,omitempty"`
	Requested  time.Time  `json:"requested_at"`
	Decided    *time.Time `json:"decided_at,omitempty"`
}

func (d Dispatcher) ListCalls(ctx context.Context, orgID, taskID string) ([]CallRecord, error) {
	rows, err := d.Pool.Query(ctx, `SELECT id,task_id,intent_id,run_id,name,input,status,policy_reason,summary,output,scope_version,created_at,completed_at FROM tool_calls WHERE organization_id=$1 AND task_id=$2 ORDER BY created_at DESC,id DESC LIMIT 200`, orgID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CallRecord{}
	for rows.Next() {
		var item CallRecord
		if err := rows.Scan(&item.ID, &item.TaskID, &item.IntentID, &item.RunID, &item.Name, &item.Input, &item.Status, &item.PolicyReason, &item.Summary, &item.Output, &item.ScopeVersion, &item.CreatedAt, &item.CompletedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (d Dispatcher) ListApprovals(ctx context.Context, orgID string) ([]Approval, error) {
	rows, err := d.Pool.Query(ctx, `SELECT a.id,a.tool_call_id,c.task_id,c.name,a.status,a.reason,a.requested_at,a.decided_at FROM tool_approvals a JOIN tool_calls c ON c.id=a.tool_call_id WHERE a.organization_id=$1 ORDER BY a.requested_at DESC,a.id DESC LIMIT 200`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Approval{}
	for rows.Next() {
		var item Approval
		if err := rows.Scan(&item.ID, &item.ToolCallID, &item.TaskID, &item.ToolName, &item.Status, &item.Reason, &item.Requested, &item.Decided); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (d Dispatcher) DecideApproval(ctx context.Context, orgID, actorID, approvalID string, approve bool, reason string) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var callID, taskID, intentID, state, callState string
	var attempt int
	err = tx.QueryRow(ctx, `SELECT a.tool_call_id,c.task_id,c.intent_id,a.status,c.status,i.attempt FROM tool_approvals a JOIN tool_calls c ON c.id=a.tool_call_id JOIN task_intents i ON i.id=c.intent_id WHERE a.id=$1 AND a.organization_id=$2 FOR UPDATE OF a,c,i`, approvalID, orgID).Scan(&callID, &taskID, &intentID, &state, &callState, &attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return task.ErrNotFound
	}
	if err != nil {
		return err
	}
	if state != "pending" || callState != "policy_check" {
		return task.ErrConflict
	}
	if approve && attempt >= 20 {
		return task.ErrConflict
	}
	decision := "rejected"
	if approve {
		decision = "approved"
	}
	if _, err = tx.Exec(ctx, `UPDATE tool_approvals SET status=$2,decided_by=$3,reason=$4,decided_at=now() WHERE id=$1`, approvalID, decision, actorID, reason); err != nil {
		return err
	}
	if approve {
		if _, err = tx.Exec(ctx, `UPDATE task_intents SET status='open',blocking_reason=NULL,max_attempts=GREATEST(max_attempts,attempt+1),ready_at=now(),updated_at=now() WHERE id=$1 AND status='blocked'`, intentID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE exploration_nodes SET state='open',updated_at=now() WHERE id=$1 AND state='blocked'`, intentID); err != nil {
			return err
		}
	} else if _, err = tx.Exec(ctx, `UPDATE tool_calls SET status='blocked',policy_reason=$2,summary=$2,completed_at=now() WHERE id=$1`, callID, "审批拒绝："+reason); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,$4,'tool_approval',$5,$6)`, identity.NewID(), orgID, actorID, "tool.approval_"+decision, approvalID, map[string]any{"reason": reason, "tool_call_id": callID}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,$4,$5)`, identity.NewID(), orgID, taskID, "tool.approval_"+decision, map[string]any{"tool_call_id": callID, "intent_id": intentID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (d Dispatcher) ApprovedPending(ctx context.Context, orgID, taskID, intentID string) ([]Call, error) {
	rows, err := d.Pool.Query(ctx, `SELECT c.name,c.input,c.idempotency_key FROM tool_calls c JOIN tool_approvals a ON a.tool_call_id=c.id WHERE c.organization_id=$1 AND c.task_id=$2 AND c.intent_id=$3 AND c.status='policy_check' AND a.status='approved' ORDER BY c.created_at,c.id`, orgID, taskID, intentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Call{}
	for rows.Next() {
		var item Call
		if err := rows.Scan(&item.Name, &item.Input, &item.IdempotencyKey); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (d Dispatcher) SetBinding(ctx context.Context, orgID, actorID, name string, enabled bool) error {
	if !known(name) {
		return task.ErrInvalid
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO tool_bindings(organization_id,agent_role,tool_name,enabled) VALUES($1,'worker',$2,$3) ON CONFLICT(organization_id,agent_role,tool_name) DO UPDATE SET enabled=excluded.enabled,updated_at=now()`, orgID, name, enabled); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,detail) VALUES($1,$2,$3,'tool.binding_changed','tool_binding',$4)`, identity.NewID(), orgID, actorID, map[string]any{"name": name, "enabled": enabled}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (d Dispatcher) Bindings(ctx context.Context, orgID string) (map[string]bool, error) {
	items := map[string]bool{}
	for _, name := range d.AvailableNames() {
		items[name] = true
	}
	rows, err := d.Pool.Query(ctx, `SELECT tool_name,enabled FROM tool_bindings WHERE organization_id=$1 AND agent_role='worker'`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var enabled bool
		if err := rows.Scan(&name, &enabled); err != nil {
			return nil, err
		}
		items[name] = enabled
	}
	return items, rows.Err()
}
