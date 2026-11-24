package task

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/jackc/pgx/v5"
)

type DraftPatch struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Version     int64   `json:"version"`
}

func (s Store) UpdateDraft(ctx context.Context, orgID, actorID, id string, p DraftPatch) (Task, error) {
	if p.Version < 1 {
		return Task{}, ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback(ctx)
	var current Task
	current, err = scanTask(tx.QueryRow(ctx, "SELECT "+taskColumns+" FROM tasks WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL FOR UPDATE", id, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	if current.Version != p.Version || current.Status != "draft" {
		return Task{}, ErrConflict
	}
	if p.Title == nil && p.Description == nil {
		return Task{}, ErrInvalid
	}
	if p.Title != nil {
		current.Title = strings.TrimSpace(*p.Title)
		if n := utf8.RuneCountInString(current.Title); n < 1 || n > 200 {
			return Task{}, ErrInvalid
		}
	}
	if p.Description != nil {
		current.Description = *p.Description
		if utf8.RuneCountInString(current.Description) > 20000 {
			return Task{}, ErrInvalid
		}
	}
	updated, err := scanTask(tx.QueryRow(ctx, "UPDATE tasks SET title=$2,description=$3,version=version+1,updated_at=now() WHERE id=$1 RETURNING "+taskColumns, id, current.Title, current.Description))
	if err != nil {
		return Task{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE exploration_nodes SET summary=$2,version=version+1,updated_at=now() WHERE task_id=$1 AND kind='origin'", id, current.Description); err != nil {
		return Task{}, err
	}
	if err := emit(ctx, tx, orgID, id, "task.updated", map[string]any{"task_id": id, "version": updated.Version}); err != nil {
		return Task{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'task.updated','task',$4)`, identity.NewID(), orgID, actorID, id); err != nil {
		return Task{}, err
	}
	return updated, tx.Commit(ctx)
}

func (s Store) Start(ctx context.Context, orgID, id string, version int64) error {
	task, err := s.GetTaskOnly(ctx, orgID, id)
	if err != nil {
		return err
	}
	if task.Version != version || task.Status != "draft" {
		return ErrConflict
	}
	return ErrModelUnavailable
}
