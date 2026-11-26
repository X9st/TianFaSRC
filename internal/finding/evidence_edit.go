package finding

import (
	"context"
	"errors"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
)

func (s Store) UpdateBinding(ctx context.Context, orgID, actorID, bindingID, role, note string) (Evidence, error) {
	switch role {
	case "baseline", "proof", "verification", "supporting":
	default:
		return Evidence{}, task.ErrInvalid
	}
	if len(note) > 4000 {
		return Evidence{}, task.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Evidence{}, err
	}
	defer tx.Rollback(ctx)
	var findingID, taskID string
	err = tx.QueryRow(ctx, `SELECT f.id,f.task_id FROM evidence_bindings b JOIN findings f ON f.id=b.finding_id WHERE b.id=$1 AND f.organization_id=$2 FOR UPDATE OF f`, bindingID, orgID).Scan(&findingID, &taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Evidence{}, task.ErrNotFound
	}
	if err != nil {
		return Evidence{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE evidence_bindings SET role=$2,note=$3 WHERE id=$1`, bindingID, role, note); err != nil {
		return Evidence{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE findings SET evidence_version=evidence_version+1,version=version+1,updated_at=now() WHERE id=$1`, findingID); err != nil {
		return Evidence{}, err
	}
	if err := audit(ctx, tx, orgID, actorID, "evidence.binding_changed", "finding", findingID, map[string]any{"binding_id": bindingID, "role": role}); err != nil {
		return Evidence{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,'evidence.binding_changed',$4)`, identity.NewID(), orgID, taskID, map[string]any{"finding_id": findingID}); err != nil {
		return Evidence{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Evidence{}, err
	}
	return s.evidenceByID(ctx, orgID, bindingID, false)
}

func (s Store) Reorder(ctx context.Context, orgID, actorID, findingID string, ordered []string) ([]Evidence, error) {
	if len(ordered) > 500 {
		return nil, task.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var taskID string
	err = tx.QueryRow(ctx, `SELECT task_id FROM findings WHERE id=$1 AND organization_id=$2 FOR UPDATE`, findingID, orgID).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, task.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM evidence_bindings WHERE finding_id=$1`, findingID)
	if err != nil {
		return nil, err
	}
	actual := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		actual[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(actual) != len(ordered) {
		return nil, task.ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range ordered {
		if !actual[id] || seen[id] {
			return nil, task.ErrInvalid
		}
		seen[id] = true
	}
	// Move every position out of the final range before assigning the requested order.
	if _, err := tx.Exec(ctx, `UPDATE evidence_bindings SET position=position+1000000 WHERE finding_id=$1`, findingID); err != nil {
		return nil, err
	}
	for position, id := range ordered {
		if _, err := tx.Exec(ctx, `UPDATE evidence_bindings SET position=$2 WHERE id=$1`, id, position); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE findings SET evidence_version=evidence_version+1,version=version+1,updated_at=now() WHERE id=$1`, findingID); err != nil {
		return nil, err
	}
	if err := audit(ctx, tx, orgID, actorID, "evidence.reordered", "finding", findingID, map[string]any{"binding_ids": ordered}); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,'evidence.reordered',$4)`, identity.NewID(), orgID, taskID, map[string]any{"finding_id": findingID}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Evidence(ctx, orgID, findingID)
}
