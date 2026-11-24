package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/X9st/TianFaSRC/internal/identity"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrSessionExpired     = errors.New("session expired")
	ErrAlreadyInitialized = errors.New("organization already initialized")
	ErrInvalidInput       = errors.New("invalid user input")
	ErrLastAdmin          = errors.New("cannot remove the last active admin")
)

type User struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Email          string    `json:"email"`
	Role           string    `json:"role"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
}

type Session struct {
	User
}

type Store struct{ Pool *pgxpool.Pool }

const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (s Store) InitAdmin(ctx context.Context, email, password string) (User, error) {
	email, err := validEmail(email)
	if err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, ErrInvalidInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x5449414e4641)); err != nil {
		return User{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&count); err != nil {
		return User{}, err
	}
	if count != 0 {
		return User{}, ErrAlreadyInitialized
	}
	orgID, userID := identity.NewID(), identity.NewID()
	if _, err := tx.Exec(ctx, "INSERT INTO organizations(id,name) VALUES($1,$2)", orgID, "天罚安全团队"); err != nil {
		return User{}, err
	}
	var user User
	if err := tx.QueryRow(ctx, `INSERT INTO users(id,organization_id,email,password_hash,role)
		VALUES($1,$2,$3,$4,'admin') RETURNING id,organization_id,email,role,status,created_at`,
		userID, orgID, email, hash).Scan(&user.ID, &user.OrganizationID, &user.Email, &user.Role, &user.Status, &user.CreatedAt); err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id)
		VALUES($1,$2,$3,'organization.initialized','user',$3)`, identity.NewID(), orgID, userID); err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,event_type,payload) VALUES($1,$2,'organization.initialized',$3)`, identity.NewID(), orgID, map[string]any{"schema_version": 1, "data": map[string]any{"user_id": userID}}); err != nil {
		return User{}, err
	}
	return user, tx.Commit(ctx)
}

func (s Store) Login(ctx context.Context, email, password string) (User, string, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var user User
	var hash string
	err := s.Pool.QueryRow(ctx, `SELECT id,organization_id,email,role,status,created_at,password_hash
		FROM users WHERE email=$1`, email).Scan(&user.ID, &user.OrganizationID, &user.Email, &user.Role, &user.Status, &user.CreatedAt, &hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", "", err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		VerifyPassword(dummyPasswordHash, password)
		return User{}, "", "", ErrInvalidCredentials
	}
	validPassword := VerifyPassword(hash, password)
	if user.Status != "active" || !validPassword {
		return User{}, "", "", ErrInvalidCredentials
	}
	token, err := randomToken()
	if err != nil {
		return User{}, "", "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return User{}, "", "", err
	}
	tokenHash, csrfHash := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(csrf))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return User{}, "", "", err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO sessions(id,user_id,token_hash,csrf_hash,expires_at)
		VALUES($1,$2,$3,$4,now()+interval '8 hours')`, identity.NewID(), user.ID, tokenHash[:], csrfHash[:])
	if err != nil {
		return User{}, "", "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'auth.login','user',$3)`, identity.NewID(), user.OrganizationID, user.ID); err != nil {
		return User{}, "", "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, "", "", err
	}
	return user, token, csrf, nil
}

func (s Store) FromToken(ctx context.Context, token string) (Session, error) {
	if len(token) != 43 {
		return Session{}, ErrSessionExpired
	}
	hash := sha256.Sum256([]byte(token))
	var session Session
	err := s.Pool.QueryRow(ctx, `SELECT u.id,u.organization_id,u.email,u.role,u.status,u.created_at
		FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now() AND u.status='active'`, hash[:]).Scan(
		&session.ID, &session.OrganizationID, &session.Email, &session.Role, &session.Status, &session.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSessionExpired
	}
	return session, err
}

func (s Store) CheckCSRF(ctx context.Context, token, csrf string) bool {
	if len(csrf) != 43 || len(token) != 43 {
		return false
	}
	tokenHash, csrfHash := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(csrf))
	var valid bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions
		WHERE token_hash=$1 AND csrf_hash=$2 AND revoked_at IS NULL AND expires_at>now())`, tokenHash[:], csrfHash[:]).Scan(&valid)
	return err == nil && valid
}

func (s Store) Logout(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.Pool.Exec(ctx, "UPDATE sessions SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL", hash[:])
	return err
}

func (s Store) CreateUser(ctx context.Context, orgID, actorID, email, password, role string) (User, error) {
	var err error
	email, err = validEmail(email)
	if err != nil {
		return User{}, err
	}
	if role != "admin" && role != "operator" && role != "reviewer" && role != "viewer" {
		return User{}, ErrInvalidInput
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, ErrInvalidInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)
	var user User
	id := identity.NewID()
	err = tx.QueryRow(ctx, `INSERT INTO users(id,organization_id,email,password_hash,role)
		VALUES($1,$2,$3,$4,$5) RETURNING id,organization_id,email,role,status,created_at`,
		id, orgID, email, hash, role).Scan(&user.ID, &user.OrganizationID, &user.Email, &user.Role, &user.Status, &user.CreatedAt)
	if err != nil {
		return User{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id)
		VALUES($1,$2,$3,'user.created','user',$4)`, identity.NewID(), orgID, actorID, id)
	if err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,event_type,payload) VALUES($1,$2,'user.created',$3)`, identity.NewID(), orgID, map[string]any{"schema_version": 1, "data": map[string]any{"user_id": id}}); err != nil {
		return User{}, err
	}
	return user, tx.Commit(ctx)
}

func (s Store) UpdateUser(ctx context.Context, orgID, actorID, userID string, role, status *string) (User, error) {
	if role == nil && status == nil {
		return User{}, ErrInvalidInput
	}
	if role != nil && *role != "admin" && *role != "operator" && *role != "reviewer" && *role != "viewer" {
		return User{}, ErrInvalidInput
	}
	if status != nil && *status != "active" && *status != "disabled" {
		return User{}, ErrInvalidInput
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)
	var lockedOrg string
	if err := tx.QueryRow(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, orgID).Scan(&lockedOrg); err != nil {
		return User{}, err
	}
	var current User
	err = tx.QueryRow(ctx, `SELECT id,organization_id,email,role,status,created_at FROM users WHERE id=$1 AND organization_id=$2 FOR UPDATE`, userID, orgID).Scan(&current.ID, &current.OrganizationID, &current.Email, &current.Role, &current.Status, &current.CreatedAt)
	if err != nil {
		return User{}, err
	}
	nextRole, nextStatus := current.Role, current.Status
	if role != nil {
		nextRole = *role
	}
	if status != nil {
		nextStatus = *status
	}
	if current.Role == "admin" && current.Status == "active" && (nextRole != "admin" || nextStatus != "active") {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE organization_id=$1 AND role='admin' AND status='active'`, orgID).Scan(&count); err != nil {
			return User{}, err
		}
		if count <= 1 {
			return User{}, ErrLastAdmin
		}
	}
	var updated User
	err = tx.QueryRow(ctx, `UPDATE users SET role=$2,status=$3,updated_at=now() WHERE id=$1 RETURNING id,organization_id,email,role,status,created_at`, userID, nextRole, nextStatus).Scan(&updated.ID, &updated.OrganizationID, &updated.Email, &updated.Role, &updated.Status, &updated.CreatedAt)
	if err != nil {
		return User{}, err
	}
	if nextStatus == "disabled" {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
			return User{}, err
		}
	}
	detail := map[string]any{"schema_version": 1, "old_role": current.Role, "new_role": nextRole, "old_status": current.Status, "new_status": nextStatus}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'user.updated','user',$4,$5)`, identity.NewID(), orgID, actorID, userID, detail); err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,event_type,payload) VALUES($1,$2,'user.updated',$3)`, identity.NewID(), orgID, map[string]any{"schema_version": 1, "data": map[string]any{"user_id": userID, "role": nextRole, "status": nextStatus}}); err != nil {
		return User{}, err
	}
	return updated, tx.Commit(ctx)
}

func validEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	parsed, err := mail.ParseAddress(email)
	if len(email) < 3 || len(email) > 254 || err != nil || parsed.Address != email {
		return "", ErrInvalidInput
	}
	return email, nil
}

func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
