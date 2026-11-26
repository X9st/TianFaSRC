package recordproxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TokenStore struct{ Pool *pgxpool.Pool }
type Token struct {
	Value     string    `json:"token"`
	TaskID    string    `json:"task_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s TokenStore) Issue(ctx context.Context, orgID, actorID, taskID string) (Token, error) {
	var valid bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND organization_id=$2 AND status IN ('draft','queued','running','paused') AND deleted_at IS NULL)`, taskID, orgID).Scan(&valid); err != nil {
		return Token{}, err
	}
	if !valid {
		return Token{}, task.ErrNotFound
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Token{}, err
	}
	value := base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(value))
	expiry := time.Now().Add(time.Hour)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Token{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO proxy_tokens(id,organization_id,task_id,created_by,token_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, identity.NewID(), orgID, taskID, actorID, hash[:], expiry); err != nil {
		return Token{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'proxy.token_issued','task',$4,$5)`, identity.NewID(), orgID, actorID, taskID, map[string]any{"expires_at": expiry}); err != nil {
		return Token{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Token{}, err
	}
	return Token{Value: value, TaskID: taskID, ExpiresAt: expiry}, nil
}

type Session struct{ OrganizationID, TaskID string }

func (s TokenStore) Authenticate(ctx context.Context, value string) (Session, error) {
	if len(value) != 43 {
		return Session{}, errors.New("代理令牌无效")
	}
	hash := sha256.Sum256([]byte(value))
	var session Session
	err := s.Pool.QueryRow(ctx, `SELECT p.organization_id,p.task_id FROM proxy_tokens p JOIN tasks t ON t.id=p.task_id WHERE p.token_hash=$1 AND p.expires_at>now() AND p.revoked_at IS NULL AND t.deleted_at IS NULL AND t.status IN ('draft','queued','running','paused')`, hash[:]).Scan(&session.OrganizationID, &session.TaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, errors.New("代理令牌无效或已过期")
	}
	return session, err
}
