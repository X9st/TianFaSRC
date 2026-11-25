package llm

import (
	"context"
	"errors"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/jackc/pgx/v5"
)

type Chain struct {
	Default    []string `json:"default"`
	Task       []string `json:"task,omitempty"`
	Decomposer []string `json:"decomposer,omitempty"`
	Planner    []string `json:"planner,omitempty"`
	Worker     []string `json:"worker,omitempty"`
}

func (s Store) SetDefaultChain(ctx context.Context, orgID, actorID string, ids []string) error {
	return s.setChain(ctx, orgID, actorID, "", "", ids)
}
func (s Store) SetTaskChain(ctx context.Context, orgID, actorID, taskID, role string, ids []string) error {
	if role != "" && role != "decomposer" && role != "planner" && role != "worker" {
		return ErrInvalid
	}
	return s.setChain(ctx, orgID, actorID, taskID, role, ids)
}
func (s Store) setChain(ctx context.Context, orgID, actorID, taskID, role string, ids []string) error {
	if len(ids) > 20 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if taskID != "" {
		var status string
		err = tx.QueryRow(ctx, `SELECT status FROM tasks WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL FOR UPDATE`, taskID, orgID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "draft" {
			return ErrConflict
		}
	}
	for _, id := range ids {
		var enabled bool
		err = tx.QueryRow(ctx, `SELECT enabled AND credential_ref IS NOT NULL FROM llm_profiles WHERE id=$1 AND organization_id=$2`, id, orgID).Scan(&enabled)
		if errors.Is(err, pgx.ErrNoRows) || !enabled {
			return ErrInvalid
		}
		if err != nil {
			return err
		}
	}
	if taskID != "" && role != "" && len(ids) > 0 {
		base, err := scanIDs(tx.Query(ctx, `SELECT profile_id FROM task_llm_chain WHERE task_id=$1 ORDER BY position`, taskID))
		if err != nil {
			return err
		}
		if len(base) == 0 {
			base, err = scanIDs(tx.Query(ctx, `SELECT profile_id FROM llm_default_chain WHERE organization_id=$1 ORDER BY position`, orgID))
			if err != nil {
				return err
			}
		}
		allowed := map[string]bool{}
		for _, id := range base {
			allowed[id] = true
		}
		for _, id := range ids {
			if !allowed[id] {
				return ErrInvalid
			}
		}
	}
	if taskID == "" {
		_, err = tx.Exec(ctx, `DELETE FROM llm_default_chain WHERE organization_id=$1`, orgID)
	} else if role == "" {
		_, err = tx.Exec(ctx, `DELETE FROM task_llm_chain WHERE task_id=$1`, taskID)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM task_agent_profile_routes WHERE task_id=$1 AND role=$2`, taskID, role)
	}
	if err != nil {
		return err
	}
	for i, id := range ids {
		if taskID == "" {
			_, err = tx.Exec(ctx, `INSERT INTO llm_default_chain(organization_id,position,profile_id) VALUES($1,$2,$3)`, orgID, i, id)
		} else if role == "" {
			_, err = tx.Exec(ctx, `INSERT INTO task_llm_chain(task_id,position,profile_id) VALUES($1,$2,$3)`, taskID, i, id)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO task_agent_profile_routes(task_id,role,position,profile_id) VALUES($1,$2,$3,$4)`, taskID, role, i, id)
		}
		if err != nil {
			return err
		}
	}
	if taskID == "" {
		_, err = tx.Exec(ctx, `DELETE FROM task_agent_profile_routes r USING tasks t WHERE r.task_id=t.id AND t.organization_id=$1 AND NOT EXISTS(SELECT 1 FROM task_llm_chain c WHERE c.task_id=t.id) AND NOT EXISTS(SELECT 1 FROM llm_default_chain d WHERE d.organization_id=$1 AND d.profile_id=r.profile_id)`, orgID)
	} else if role == "" {
		base := ids
		if len(base) == 0 {
			base, err = scanIDs(tx.Query(ctx, `SELECT profile_id FROM llm_default_chain WHERE organization_id=$1 ORDER BY position`, orgID))
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `DELETE FROM task_agent_profile_routes WHERE task_id=$1 AND NOT (profile_id::text=ANY($2::text[]))`, taskID, base)
	}
	if err != nil {
		return err
	}
	if taskID != "" {
		_, err = tx.Exec(ctx, `UPDATE tasks SET version=version+1,updated_at=now() WHERE id=$1`, taskID)
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'llm.chain_updated','llm_chain',$4)`, identity.NewID(), orgID, actorID, nilIfEmpty(taskID)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,event_type,payload) VALUES($1,$2,'llm.chain_updated',$3)`, identity.NewID(), orgID, map[string]any{"schema_version": 1, "data": map[string]any{"task_id": taskID, "role": role}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func nilIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (s Store) GetDefaultChain(ctx context.Context, orgID string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT profile_id FROM llm_default_chain WHERE organization_id=$1 ORDER BY position`, orgID)
	return scanIDs(rows, err)
}
func (s Store) GetTaskChain(ctx context.Context, orgID, taskID, role string) ([]string, error) {
	if role != "" && role != "decomposer" && role != "planner" && role != "worker" {
		return nil, ErrInvalid
	}
	var exists bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL)`, taskID, orgID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	if role == "" {
		rows, err := s.Pool.Query(ctx, `SELECT profile_id FROM task_llm_chain WHERE task_id=$1 ORDER BY position`, taskID)
		return scanIDs(rows, err)
	}
	rows, err := s.Pool.Query(ctx, `SELECT profile_id FROM task_agent_profile_routes WHERE task_id=$1 AND role=$2 ORDER BY position`, taskID, role)
	return scanIDs(rows, err)
}
func scanIDs(rows pgx.Rows, err error) ([]string, error) {
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
	return ids, rows.Err()
}
func (s Store) ResolvedChain(ctx context.Context, orgID, taskID, role string) ([]Profile, error) {
	base, err := s.GetTaskChain(ctx, orgID, taskID, "")
	if err != nil {
		return nil, err
	}
	if len(base) == 0 {
		base, err = s.GetDefaultChain(ctx, orgID)
		if err != nil {
			return nil, err
		}
	}
	ids := base
	if role != "" {
		selected, err := s.GetTaskChain(ctx, orgID, taskID, role)
		if err != nil {
			return nil, err
		}
		if len(selected) > 0 {
			allowed := map[string]bool{}
			for _, id := range base {
				allowed[id] = true
			}
			ids = []string{}
			for _, id := range selected {
				if allowed[id] {
					ids = append(ids, id)
				}
			}
		}
	}
	out := []Profile{}
	for _, id := range ids {
		p, err := s.Get(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		if p.Enabled && p.CredentialRef != nil {
			out = append(out, p)
		}
	}
	return out, nil
}
