package task

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/jackc/pgx/v5"
)

type Activity struct {
	ID         string    `json:"id"`
	EventID    string    `json:"event_id"`
	TaskID     string    `json:"task_id"`
	Kind       string    `json:"kind"`
	Summary    string    `json:"summary"`
	OccurredAt time.Time `json:"occurred_at"`
}

// ProjectActivities can safely be retried by multiple processes. The claim,
// consumption marker and projection commit together.
func (s Store) ProjectActivities(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT e.id,e.task_id,e.event_type,e.payload,e.occurred_at
 FROM domain_events e WHERE e.task_id IS NOT NULL AND NOT EXISTS
 (SELECT 1 FROM event_consumptions c WHERE c.consumer_name='activity' AND c.event_id=e.id)
 ORDER BY e.occurred_at,e.id LIMIT $1 FOR UPDATE OF e SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	type event struct {
		id, taskID, kind string
		payload          json.RawMessage
		at               time.Time
	}
	events := []event{}
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.id, &e.taskID, &e.kind, &e.payload, &e.at); err != nil {
			rows.Close()
			return 0, err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, e := range events {
		tag, err := tx.Exec(ctx, `INSERT INTO event_consumptions(consumer_name,event_id) VALUES('activity',$1) ON CONFLICT DO NOTHING`, e.id)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		summary := e.kind
		var body struct {
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(e.payload, &body) == nil {
			if name, ok := body.Data["title"].(string); ok && name != "" {
				summary = name
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO activities(id,event_id,task_id,kind,summary,occurred_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(event_id) DO NOTHING`, identity.NewID(), e.id, e.taskID, e.kind, summary, e.at); err != nil {
			return 0, err
		}
	}
	return len(events), tx.Commit(ctx)
}

func (s Store) ListActivities(ctx context.Context, orgID, taskID string, limit int) ([]Activity, error) {
	if _, err := s.GetTaskOnly(ctx, orgID, taskID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		limit = 30
	}
	rows, err := s.Pool.Query(ctx, `SELECT a.id,a.event_id,a.task_id,a.kind,a.summary,a.occurred_at FROM activities a WHERE a.task_id=$1 ORDER BY a.occurred_at DESC,a.id DESC LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.EventID, &a.TaskID, &a.Kind, &a.Summary, &a.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s Store) GetNode(ctx context.Context, orgID, nodeID string) (Node, error) {
	var n Node
	err := s.Pool.QueryRow(ctx, `SELECT n.id,n.kind,n.state,n.summary,n.source_intent_id,n.run_id,n.tool_call_id,n.created_at FROM exploration_nodes n JOIN tasks t ON t.id=n.task_id WHERE n.id=$1 AND t.organization_id=$2 AND t.deleted_at IS NULL`, nodeID, orgID).Scan(&n.ID, &n.Kind, &n.State, &n.Summary, &n.SourceIntentID, &n.RunID, &n.ToolCallID, &n.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	return n, err
}
