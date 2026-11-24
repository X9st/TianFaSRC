package task

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/jackc/pgx/v5"
)

func lockDraft(ctx context.Context, tx pgx.Tx, orgID, taskID string, version int64) (Task, error) {
	current, err := scanTask(tx.QueryRow(ctx, "SELECT "+taskColumns+" FROM tasks WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL FOR UPDATE", taskID, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	if current.Status != "draft" || current.Version != version {
		return Task{}, ErrConflict
	}
	return current, nil
}

func (s Store) AddGoal(ctx context.Context, orgID, actorID, taskID string, version int64, input GoalInput) (Detail, error) {
	input.Text = strings.TrimSpace(input.Text)
	if utf8.RuneCountInString(input.Text) < 1 || utf8.RuneCountInString(input.Text) > 4000 || input.Priority < 0 || input.Priority > 10 {
		return Detail{}, ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := lockDraft(ctx, tx, orgID, taskID, version); err != nil {
		return Detail{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM task_goals WHERE task_id=$1", taskID).Scan(&count); err != nil {
		return Detail{}, err
	}
	if count >= 50 {
		return Detail{}, ErrInvalid
	}
	id := identity.NewID()
	if _, err := tx.Exec(ctx, "INSERT INTO task_goals(id,task_id,text,priority) VALUES($1,$2,$3,$4)", id, taskID, input.Text, input.Priority); err != nil {
		return Detail{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO exploration_nodes(id,task_id,kind,state,summary) VALUES($1,$2,'goal','open',$3)", id, taskID, input.Text); err != nil {
		return Detail{}, err
	}
	var originID string
	if err := tx.QueryRow(ctx, "SELECT id FROM exploration_nodes WHERE task_id=$1 AND kind='origin'", taskID).Scan(&originID); err != nil {
		return Detail{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind) VALUES($1,$2,$3,'derived_from')", taskID, originID, id); err != nil {
		return Detail{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE tasks SET version=version+1,updated_at=now() WHERE id=$1", taskID); err != nil {
		return Detail{}, err
	}
	if err := emit(ctx, tx, orgID, taskID, "goal.added", map[string]any{"goal_id": id, "text": input.Text}); err != nil {
		return Detail{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'goal.added','goal',$4)`, identity.NewID(), orgID, actorID, id); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, orgID, taskID)
}

func (s Store) AddConstraint(ctx context.Context, orgID, actorID, taskID string, version int64, input ConstraintInput) (Detail, error) {
	input.Text = strings.TrimSpace(input.Text)
	if utf8.RuneCountInString(input.Text) < 1 || utf8.RuneCountInString(input.Text) > 4000 || (input.Effect != "allow" && input.Effect != "deny") {
		return Detail{}, ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := lockDraft(ctx, tx, orgID, taskID, version); err != nil {
		return Detail{}, err
	}
	id := identity.NewID()
	if _, err := tx.Exec(ctx, "INSERT INTO task_constraints(id,task_id,effect,text,source) VALUES($1,$2,$3,$4,'user')", id, taskID, input.Effect, input.Text); err != nil {
		return Detail{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE tasks SET version=version+1,updated_at=now() WHERE id=$1", taskID); err != nil {
		return Detail{}, err
	}
	if err := emit(ctx, tx, orgID, taskID, "constraint.added", map[string]any{"constraint_id": id, "effect": input.Effect}); err != nil {
		return Detail{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'constraint.added','constraint',$4,$5)`, identity.NewID(), orgID, actorID, id, map[string]any{"schema_version": 1, "text": input.Text, "effect": input.Effect}); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, orgID, taskID)
}
