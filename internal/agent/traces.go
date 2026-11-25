package agent

import (
	"context"
	"strings"
	"time"
)

type TraceHit struct {
	RunID     string    `json:"run_id"`
	IntentID  string    `json:"intent_id"`
	Ordinal   int       `json:"ordinal"`
	Role      string    `json:"role"`
	Excerpt   string    `json:"excerpt"`
	CreatedAt time.Time `json:"created_at"`
}

// SearchTraces is scoped to one task and omits the caller's current Run.
// GetRun serves the full transcript for a selected hit.
func (s Scheduler) SearchTraces(ctx context.Context, orgID, taskID, query, excludeRunID string, limit int) ([]TraceHit, error) {
	if limit < 1 || limit > 100 {
		limit = 30
	}
	rows, err := s.Engine.Pool.Query(ctx, `SELECT r.id,r.intent_id,m.ordinal,m.role,left(m.content,500),m.created_at FROM agent_messages m JOIN agent_runs r ON r.id=m.run_id JOIN tasks t ON t.id=r.task_id WHERE r.task_id=$1 AND t.organization_id=$2 AND r.role='worker' AND ($3='' OR r.id::text<>$3) AND ($4='' OR m.content ILIKE '%'||$4||'%') ORDER BY m.created_at DESC,m.id DESC LIMIT $5`, taskID, orgID, excludeRunID, strings.TrimSpace(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []TraceHit{}
	for rows.Next() {
		var x TraceHit
		if err := rows.Scan(&x.RunID, &x.IntentID, &x.Ordinal, &x.Role, &x.Excerpt, &x.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, x)
	}
	return items, rows.Err()
}
