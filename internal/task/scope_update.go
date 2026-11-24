package task

import (
	"context"
	"errors"
	"fmt"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/jackc/pgx/v5"
)

func NormalizeScopeSet(entries []ScopeEntryInput) ([]ScopeEntry, error) {
	if len(entries) == 0 || len(entries) > 200 {
		return nil, ErrInvalid
	}
	seen := map[string]bool{}
	allow := 0
	out := make([]ScopeEntry, 0, len(entries))
	for _, entry := range entries {
		n, err := NormalizeScope(entry)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		key := fmt.Sprintf("%s/%s/%s/%v", n.Effect, n.Kind, n.NormalizedValue, n.Ports)
		if seen[key] {
			return nil, fmt.Errorf("%w: duplicate scope entry", ErrInvalid)
		}
		seen[key] = true
		if n.Effect == "allow" {
			allow++
		}
		out = append(out, n)
	}
	if allow == 0 {
		return nil, fmt.Errorf("%w: at least one allow scope is required", ErrInvalid)
	}
	return out, nil
}

func (s Store) ReplaceScope(ctx context.Context, orgID, actorID, id string, version int64, entries []ScopeEntryInput) (Detail, error) {
	normalized, err := NormalizeScopeSet(entries)
	if err != nil {
		return Detail{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback(ctx)
	var current Task
	current, err = scanTask(tx.QueryRow(ctx, "SELECT "+taskColumns+" FROM tasks WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL FOR UPDATE", id, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	if current.Version != version || current.Status != "draft" {
		return Detail{}, ErrConflict
	}
	next := current.ScopeVersion + 1
	for _, entry := range normalized {
		if _, err := tx.Exec(ctx, `INSERT INTO task_scopes(id,task_id,scope_version,effect,kind,value,normalized_value,ports,path_prefix,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, identity.NewID(), id, next, entry.Effect, entry.Kind, entry.Value, entry.NormalizedValue, entry.Ports, entry.PathPrefix, actorID); err != nil {
			return Detail{}, err
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE tasks SET scope_version=$2,version=version+1,updated_at=now() WHERE id=$1", id, next); err != nil {
		return Detail{}, err
	}
	if err := emit(ctx, tx, orgID, id, "scope.changed", map[string]any{"task_id": id, "scope_version": next}); err != nil {
		return Detail{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'scope.changed','task',$4)`, identity.NewID(), orgID, actorID, id); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, orgID, id)
}
