package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Todo struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	Status    string    `json:"status"`
	DependsOn []string  `json:"depends_on"`
	IntentID  *string   `json:"intent_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (s Scheduler) ListTodos(ctx context.Context, orgID, taskID string) ([]Todo, error) {
	rows, err := s.Engine.Pool.Query(ctx, `SELECT d.id,d.title,d.detail,d.status,COALESCE(d.depends_on,'{}'::uuid[]),d.intent_id,d.created_at FROM task_todos d JOIN tasks t ON t.id=d.task_id WHERE d.task_id=$1 AND t.organization_id=$2 ORDER BY d.created_at,d.id`, taskID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Todo{}
	for rows.Next() {
		var x Todo
		if err := rows.Scan(&x.ID, &x.Title, &x.Detail, &x.Status, &x.DependsOn, &x.IntentID, &x.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, x)
	}
	return items, rows.Err()
}

func (s Scheduler) AddTodo(ctx context.Context, orgID, actorID, taskID, title, detail string, deps []string) (Todo, error) {
	title, detail = strings.TrimSpace(title), strings.TrimSpace(detail)
	if title == "" || len(title) > 300 || len(detail) > 4000 || len(deps) > 20 {
		return Todo{}, task.ErrInvalid
	}
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return Todo{}, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM tasks WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL FOR UPDATE`, taskID, orgID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return Todo{}, task.ErrNotFound
	} else if err != nil {
		return Todo{}, err
	}
	if status == "cancelled" || status == "completed" {
		return Todo{}, task.ErrConflict
	}
	for _, dep := range deps {
		var state string
		if err = tx.QueryRow(ctx, `SELECT status FROM task_todos WHERE id=$1 AND task_id=$2`, dep, taskID).Scan(&state); err != nil {
			return Todo{}, task.ErrInvalid
		}
	}
	id := identity.NewID()
	_, err = tx.Exec(ctx, `INSERT INTO task_todos(id,task_id,title,detail,depends_on) VALUES($1,$2,$3,$4,$5)`, id, taskID, title, detail, deps)
	if err != nil {
		return Todo{}, err
	}
	if err = event(ctx, tx, orgID, taskID, "todo.created", map[string]any{"todo_id": id}); err != nil {
		return Todo{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'todo.created','todo',$4)`, identity.NewID(), orgID, actorID, id); err != nil {
		return Todo{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Todo{}, err
	}
	return Todo{ID: id, Title: title, Detail: detail, Status: "open", DependsOn: deps, CreatedAt: time.Now()}, nil
}

func (s Scheduler) CompleteTodo(ctx context.Context, orgID, actorID, taskID, todoID string) error {
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE task_todos d SET status='done',updated_at=now() FROM tasks t WHERE d.id=$1 AND d.task_id=$2 AND t.id=d.task_id AND t.organization_id=$3 AND d.status='open' AND NOT EXISTS(SELECT 1 FROM unnest(d.depends_on) dep JOIN task_todos p ON p.id=dep WHERE p.status<>'done')`, todoID, taskID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return task.ErrConflict
	}
	if err = event(ctx, tx, orgID, taskID, "todo.done", map[string]any{"todo_id": todoID}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'todo.done','todo',$4)`, identity.NewID(), orgID, actorID, todoID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Scheduler) Steer(ctx context.Context, orgID, actorID, taskID, intentID, message string) error {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > 4000 {
		return task.ErrInvalid
	}
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	err = tx.QueryRow(ctx, `SELECT i.status FROM task_intents i JOIN tasks t ON t.id=i.task_id WHERE i.id=$1 AND i.task_id=$2 AND t.organization_id=$3 AND t.status IN ('queued','running','paused') FOR UPDATE OF i`, intentID, taskID, orgID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return task.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "done" || status == "stopped" {
		return task.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO worker_steers(id,task_id,intent_id,actor_user_id,message) VALUES($1,$2,$3,$4,$5)`, identity.NewID(), taskID, intentID, actorID, message)
	if err != nil {
		return err
	}
	if err = event(ctx, tx, orgID, taskID, "hint.steer", map[string]any{"intent_id": intentID, "message": message}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Scheduler) StopIntent(ctx context.Context, orgID, actorID, taskID, intentID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 4000 {
		return task.ErrInvalid
	}
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE task_intents i SET status='stopped',stop_reason=$4,blocking_reason=NULL,updated_at=now() FROM tasks t WHERE i.id=$1 AND i.task_id=$2 AND t.id=i.task_id AND t.organization_id=$3 AND i.status IN ('open','leased','running','paused','blocked','exhausted')`, intentID, taskID, orgID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return task.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE exploration_nodes SET state='stopped',updated_at=now() WHERE id=$1`, intentID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='stopped',error_class='operator_stop',completed_at=now() WHERE id IN (SELECT run_id FROM worker_leases WHERE intent_id=$1) AND status='running'`, intentID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM worker_leases WHERE intent_id=$1`, intentID); err != nil {
		return err
	}
	if err = event(ctx, tx, orgID, taskID, "intent.stopped", map[string]any{"intent_id": intentID, "reason": reason}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'intent.stopped','intent',$4,$5)`, identity.NewID(), orgID, actorID, intentID, map[string]any{"reason": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Scheduler) RetryIntent(ctx context.Context, orgID, actorID, taskID, intentID string) error {
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE task_intents i SET status='open',blocking_reason=NULL,stop_reason=NULL,max_attempts=GREATEST(max_attempts,attempt+1),observation_generation=observation_generation+1,ready_at=now(),updated_at=now() FROM tasks t WHERE i.id=$1 AND i.task_id=$2 AND t.id=i.task_id AND t.organization_id=$3 AND t.status='running' AND i.status IN ('blocked','exhausted','stopped')`, intentID, taskID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return task.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE exploration_nodes SET state='open',updated_at=now() WHERE id=$1`, intentID); err != nil {
		return err
	}
	if err = event(ctx, tx, orgID, taskID, "intent.retried", map[string]any{"intent_id": intentID}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'intent.retried','intent',$4)`, identity.NewID(), orgID, actorID, intentID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Scheduler) TaskCommand(ctx context.Context, orgID, actorID, taskID, command string) error {
	from, to := "", ""
	switch command {
	case "pause":
		from, to = "running", "paused"
	case "resume":
		from, to = "paused", "queued"
	case "cancel":
		from, to = "running", "cancelled"
	default:
		return task.ErrInvalid
	}
	tx, err := s.Engine.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var tag pgconn.CommandTag
	if command == "cancel" {
		tag, err = tx.Exec(ctx, `UPDATE tasks SET status='cancelled',version=version+1,updated_at=now() WHERE id=$1 AND organization_id=$2 AND status IN ('queued','running','paused')`, taskID, orgID)
	} else {
		tag, err = tx.Exec(ctx, `UPDATE tasks SET status=$4,version=version+1,updated_at=now() WHERE id=$1 AND organization_id=$2 AND status=$3`, taskID, orgID, from, to)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return task.ErrConflict
	}
	if command == "resume" {
		if _, err = tx.Exec(ctx, `UPDATE task_intents SET status='open',ready_at=now(),updated_at=now() WHERE task_id=$1 AND status='paused'`, taskID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO planner_wakes(task_id,due_at,reason) VALUES($1,now(),'task.resumed') ON CONFLICT(task_id) DO UPDATE SET pending=true,due_at=now(),reason='task.resumed',updated_at=now()`, taskID); err != nil {
			return err
		}
	} else if command == "cancel" {
		if _, err = tx.Exec(ctx, `UPDATE task_intents SET status='stopped',stop_reason='任务已取消',blocking_reason=NULL,updated_at=now() WHERE task_id=$1 AND status IN ('open','paused','blocked','exhausted')`, taskID); err != nil {
			return err
		}
	} else {
		if _, err = tx.Exec(ctx, `UPDATE task_intents SET status='paused',updated_at=now() WHERE task_id=$1 AND status='open'`, taskID); err != nil {
			return err
		}
	}
	eventName := map[string]string{"pause": "task.paused", "resume": "task.resumed", "cancel": "task.cancelled"}[command]
	if err = event(ctx, tx, orgID, taskID, eventName, map[string]any{"task_id": taskID}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,$4,'task',$5)`, identity.NewID(), orgID, actorID, "task."+command, taskID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
