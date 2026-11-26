package finding

import (
	"context"
	"errors"
	"time"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
)

type Retest struct {
	ID              string     `json:"id"`
	FindingID       string     `json:"finding_id"`
	TaskID          string     `json:"task_id"`
	EvidenceVersion int64      `json:"evidence_version"`
	ScopeVersion    int64      `json:"scope_version"`
	Status          string     `json:"status"`
	Conclusion      *string    `json:"conclusion,omitempty"`
	Note            string     `json:"note"`
	CreatedAt       time.Time  `json:"created_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

func (s Store) CreateRetest(ctx context.Context, orgID, actorID, findingID string) (Retest, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Retest{}, err
	}
	defer tx.Rollback(ctx)
	var item Retest
	var status string
	err = tx.QueryRow(ctx, `SELECT f.task_id,f.evidence_version,t.scope_version,f.status FROM findings f JOIN tasks t ON t.id=f.task_id WHERE f.id=$1 AND f.organization_id=$2 FOR SHARE OF f,t`, findingID, orgID).Scan(&item.TaskID, &item.EvidenceVersion, &item.ScopeVersion, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Retest{}, task.ErrNotFound
	}
	if err != nil {
		return Retest{}, err
	}
	if status != "confirmed" && status != "fixed" {
		return Retest{}, task.ErrConflict
	}
	item.ID = identity.NewID()
	item.FindingID = findingID
	item.Status = "pending"
	if err := tx.QueryRow(ctx, `INSERT INTO retests(id,finding_id,organization_id,task_id,evidence_version,scope_version,created_by) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING created_at`, item.ID, findingID, orgID, item.TaskID, item.EvidenceVersion, item.ScopeVersion, actorID).Scan(&item.CreatedAt); err != nil {
		return Retest{}, err
	}
	if err := audit(ctx, tx, orgID, actorID, "retest.created", "retest", item.ID, map[string]any{"finding_id": findingID, "evidence_version": item.EvidenceVersion, "scope_version": item.ScopeVersion}); err != nil {
		return Retest{}, err
	}
	return item, tx.Commit(ctx)
}

func (s Store) GetRetest(ctx context.Context, orgID, id string) (Retest, error) {
	var item Retest
	err := s.Pool.QueryRow(ctx, `SELECT id,finding_id,task_id,evidence_version,scope_version,status,conclusion,note,created_at,completed_at FROM retests WHERE id=$1 AND organization_id=$2`, id, orgID).Scan(&item.ID, &item.FindingID, &item.TaskID, &item.EvidenceVersion, &item.ScopeVersion, &item.Status, &item.Conclusion, &item.Note, &item.CreatedAt, &item.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Retest{}, task.ErrNotFound
	}
	return item, err
}

func (s Store) ListRetests(ctx context.Context, orgID, findingID string) ([]Retest, error) {
	rows, err := s.Pool.Query(ctx, `SELECT r.id FROM retests r JOIN findings f ON f.id=r.finding_id WHERE r.finding_id=$1 AND f.organization_id=$2 ORDER BY r.created_at DESC,r.id DESC`, findingID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := []Retest{}
	for _, id := range ids {
		item, err := s.GetRetest(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s Store) CompleteRetest(ctx context.Context, orgID, actorID, id, conclusion, note string, bindingIDs []string) (Retest, error) {
	if conclusion != "still_vulnerable" && conclusion != "fixed" && conclusion != "inconclusive" || len(note) > 10000 || len(bindingIDs) == 0 || len(bindingIDs) > 50 {
		return Retest{}, task.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Retest{}, err
	}
	defer tx.Rollback(ctx)
	var findingID, taskID, nodeID, status string
	var createdAt time.Time
	err = tx.QueryRow(ctx, `SELECT r.finding_id,r.task_id,f.node_id,r.status,r.created_at FROM retests r JOIN findings f ON f.id=r.finding_id WHERE r.id=$1 AND r.organization_id=$2 FOR UPDATE OF r,f`, id, orgID).Scan(&findingID, &taskID, &nodeID, &status, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Retest{}, task.ErrNotFound
	}
	if err != nil {
		return Retest{}, err
	}
	if status != "pending" {
		return Retest{}, task.ErrConflict
	}
	seen := map[string]bool{}
	for _, bindingID := range bindingIDs {
		if seen[bindingID] {
			continue
		}
		seen[bindingID] = true
		var snapshotID string
		var capturedAt time.Time
		err := tx.QueryRow(ctx, `SELECT b.snapshot_id,s.captured_at FROM evidence_bindings b JOIN evidence_snapshots s ON s.id=b.snapshot_id WHERE b.id=$1 AND b.finding_id=$2 AND b.role='verification'`, bindingID, findingID).Scan(&snapshotID, &capturedAt)
		if errors.Is(err, pgx.ErrNoRows) || capturedAt.Before(createdAt) {
			return Retest{}, task.ErrInvalid
		}
		if err != nil {
			return Retest{}, err
		}
		for _, part := range []string{"request", "response"} {
			if _, err := s.ReadEvidence(ctx, orgID, bindingID, part); err != nil {
				return Retest{}, err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO retest_evidence(retest_id,snapshot_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, snapshotID); err != nil {
			return Retest{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE retests SET status='completed',conclusion=$2,note=$3,completed_at=now() WHERE id=$1`, id, conclusion, note); err != nil {
		return Retest{}, err
	}
	if conclusion == "fixed" || conclusion == "still_vulnerable" {
		newState := "confirmed"
		if conclusion == "fixed" {
			newState = "fixed"
		}
		if _, err := tx.Exec(ctx, `UPDATE findings SET status=$2,version=version+1,updated_at=now() WHERE id=$1`, findingID, newState); err != nil {
			return Retest{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE exploration_nodes SET state=$2,version=version+1,updated_at=now() WHERE id=$1`, nodeID, newState); err != nil {
			return Retest{}, err
		}
	}
	if err := audit(ctx, tx, orgID, actorID, "retest.completed", "retest", id, map[string]any{"conclusion": conclusion, "finding_id": findingID}); err != nil {
		return Retest{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,'retest.completed',$4)`, identity.NewID(), orgID, taskID, map[string]any{"retest_id": id, "conclusion": conclusion}); err != nil {
		return Retest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Retest{}, err
	}
	return s.GetRetest(ctx, orgID, id)
}
