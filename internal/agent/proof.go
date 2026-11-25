package agent

import (
	"context"
	"errors"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/jackc/pgx/v5"
)

// ProveGoal accepts only confirmed Fact nodes from a succeeded Worker run
// linked to an Intent for the same Goal. Model text and unverified nodes cannot
// satisfy the proof gate.
func (e Engine) ProveGoal(ctx context.Context, orgID, taskID, goalID string, evidenceIDs []string) error {
	if len(evidenceIDs) == 0 || len(evidenceIDs) > 100 {
		return llm.ErrInvalid
	}
	tx, err := e.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state string
	err = tx.QueryRow(ctx, `SELECT g.state FROM task_goals g JOIN tasks t ON t.id=g.task_id WHERE g.id=$1 AND g.task_id=$2 AND t.organization_id=$3 FOR UPDATE OF g`, goalID, taskID, orgID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return llm.ErrNotFound
	}
	if err != nil {
		return err
	}
	if state == "met" {
		return nil
	}
	seen := map[string]bool{}
	for _, nodeID := range evidenceIDs {
		if seen[nodeID] {
			continue
		}
		seen[nodeID] = true
		var valid bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM exploration_nodes n JOIN task_intents i ON i.id=n.source_intent_id JOIN agent_runs r ON r.id=n.run_id AND r.intent_id=i.id WHERE n.id=$1 AND n.task_id=$2 AND n.kind='fact' AND n.state='confirmed' AND n.tool_call_id IS NOT NULL AND i.goal_id=$3 AND r.role='worker' AND r.status IN ('succeeded','exhausted'))`, nodeID, taskID, goalID).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return llm.ErrInvalid
		}
		if _, err := tx.Exec(ctx, `INSERT INTO goal_proofs(goal_id,evidence_node_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, goalID, nodeID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind) VALUES($1,$2,$3,'proves') ON CONFLICT DO NOTHING`, taskID, nodeID, goalID); err != nil {
			return err
		}
	}
	if len(seen) == 0 {
		return llm.ErrInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE task_goals SET state='met',version=version+1 WHERE id=$1`, goalID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE exploration_nodes SET state='confirmed',version=version+1,updated_at=now() WHERE id=$1`, goalID); err != nil {
		return err
	}
	if err := event(ctx, tx, orgID, taskID, "goal.proven", map[string]any{"goal_id": goalID, "evidence_node_ids": evidenceIDs}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,action,resource_type,resource_id) VALUES($1,$2,'goal.proven','goal',$3)`, identity.NewID(), orgID, goalID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
