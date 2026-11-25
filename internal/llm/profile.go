package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/secret"
)

var (
	ErrInvalid            = errors.New("invalid LLM profile")
	ErrNotFound           = errors.New("LLM profile not found")
	ErrConflict           = errors.New("LLM profile conflict")
	ErrCredentialRequired = errors.New("credential must be submitted when changing the Base URL host")
)

type Profile struct {
	ID                string          `json:"id"`
	OrganizationID    string          `json:"organization_id"`
	Name              string          `json:"name"`
	Format            string          `json:"format"`
	BaseURL           string          `json:"base_url"`
	BaseURLHost       string          `json:"-"`
	Model             string          `json:"model"`
	CredentialRef     *string         `json:"credential_ref"`
	CredentialHint    *string         `json:"credential_hint"`
	Priority          int             `json:"priority"`
	Enabled           bool            `json:"enabled"`
	Stream            bool            `json:"stream"`
	ContextWindow     int             `json:"context_window"`
	MaxOutputTokens   int             `json:"max_output_tokens"`
	RatePerSecond     int             `json:"rate_per_second"`
	RatePerMinute     int             `json:"rate_per_minute"`
	ThinkingMode      string          `json:"thinking_mode"`
	ReasoningEffort   string          `json:"reasoning_effort"`
	SessionHeaderName string          `json:"session_header_name"`
	RetryPolicy       json.RawMessage `json:"retry_policy"`
	HealthState       string          `json:"health_state"`
	CircuitState      string          `json:"circuit_state"`
	CooldownUntil     *time.Time      `json:"cooldown_until"`
	LastSuccessAt     *time.Time      `json:"last_success_at"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type CredentialWrite struct {
	SecretValue string `json:"secret_value,omitempty"`
	Remove      bool   `json:"remove,omitempty"`
}
type CreateInput struct {
	Name              string          `json:"name"`
	Format            string          `json:"format"`
	BaseURL           string          `json:"base_url"`
	Model             string          `json:"model"`
	Credential        CredentialWrite `json:"credential"`
	Priority          int             `json:"priority"`
	Enabled           *bool           `json:"enabled,omitempty"`
	Stream            *bool           `json:"stream,omitempty"`
	ContextWindow     int             `json:"context_window"`
	MaxOutputTokens   int             `json:"max_output_tokens"`
	RatePerSecond     int             `json:"rate_per_second"`
	RatePerMinute     int             `json:"rate_per_minute"`
	ThinkingMode      string          `json:"thinking_mode,omitempty"`
	ReasoningEffort   string          `json:"reasoning_effort,omitempty"`
	SessionHeaderName string          `json:"session_header_name,omitempty"`
	RetryPolicy       json.RawMessage `json:"retry_policy,omitempty"`
}
type PatchInput struct {
	Name              *string          `json:"name,omitempty"`
	Format            *string          `json:"format,omitempty"`
	BaseURL           *string          `json:"base_url,omitempty"`
	Model             *string          `json:"model,omitempty"`
	Credential        *CredentialWrite `json:"credential,omitempty"`
	Priority          *int             `json:"priority,omitempty"`
	Enabled           *bool            `json:"enabled,omitempty"`
	Stream            *bool            `json:"stream,omitempty"`
	ContextWindow     *int             `json:"context_window,omitempty"`
	MaxOutputTokens   *int             `json:"max_output_tokens,omitempty"`
	RatePerSecond     *int             `json:"rate_per_second,omitempty"`
	RatePerMinute     *int             `json:"rate_per_minute,omitempty"`
	ThinkingMode      *string          `json:"thinking_mode,omitempty"`
	ReasoningEffort   *string          `json:"reasoning_effort,omitempty"`
	SessionHeaderName *string          `json:"session_header_name,omitempty"`
	RetryPolicy       json.RawMessage  `json:"retry_policy,omitempty"`
}

type Store struct {
	Pool    *pgxpool.Pool
	Secrets secret.Store
}

const profileColumns = `p.id,p.organization_id,p.name,p.format,p.base_url,p.base_url_host,p.model,p.credential_ref,s.hint,p.priority,p.enabled,p.stream,p.context_window,p.max_output_tokens,p.rate_per_second,p.rate_per_minute,p.thinking_mode,p.reasoning_effort,p.session_header_name,p.retry_policy,p.health_state,p.circuit_state,p.cooldown_until,p.last_success_at,p.created_at,p.updated_at`

func scanProfile(row pgx.Row) (Profile, error) {
	var p Profile
	err := row.Scan(&p.ID, &p.OrganizationID, &p.Name, &p.Format, &p.BaseURL, &p.BaseURLHost, &p.Model, &p.CredentialRef, &p.CredentialHint, &p.Priority, &p.Enabled, &p.Stream, &p.ContextWindow, &p.MaxOutputTokens, &p.RatePerSecond, &p.RatePerMinute, &p.ThinkingMode, &p.ReasoningEffort, &p.SessionHeaderName, &p.RetryPolicy, &p.HealthState, &p.CircuitState, &p.CooldownUntil, &p.LastSuccessAt, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func validateBaseURL(raw string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Hostname() == "" {
		return "", "", ErrInvalid
	}
	host := strings.ToLower(u.Hostname())
	if strings.ContainsAny(raw, "\\\r\n") || strings.Contains(u.Path, "..") || strings.Contains(u.EscapedPath(), "..") {
		return "", "", ErrInvalid
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(host)
		if u.Scheme != "http" || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return "", "", ErrInvalid
		}
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", "", ErrInvalid
		}
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return strings.TrimSuffix(u.String(), "/"), u.Host, nil
}

func validateProfile(p Profile) error {
	if len(p.Name) < 1 || len(p.Name) > 120 || strings.TrimSpace(p.Name) != p.Name || len(p.Model) < 1 || len(p.Model) > 200 || strings.TrimSpace(p.Model) != p.Model {
		return ErrInvalid
	}
	if p.Format != "openai-chat" && p.Format != "openai-responses" && p.Format != "anthropic" {
		return ErrInvalid
	}
	if p.ContextWindow < 1024 || p.ContextWindow > 2000000 || p.MaxOutputTokens < 1 || p.MaxOutputTokens > 1000000 || p.MaxOutputTokens+512 >= p.ContextWindow {
		return ErrInvalid
	}
	if p.RatePerSecond < 1 || p.RatePerSecond > 10000 || p.RatePerMinute < 1 || p.RatePerMinute > 1000000 {
		return ErrInvalid
	}
	if len(p.SessionHeaderName) > 100 || strings.ContainsAny(p.SessionHeaderName, "\r\n:") || len(p.ThinkingMode) > 40 || len(p.ReasoningEffort) > 40 {
		return ErrInvalid
	}
	var policy map[string]any
	if json.Unmarshal(p.RetryPolicy, &policy) != nil || policy == nil || policy["schema_version"] != float64(1) {
		return ErrInvalid
	}
	return nil
}

func (s Store) Create(ctx context.Context, orgID, actorID string, input CreateInput) (Profile, error) {
	base, host, err := validateBaseURL(input.BaseURL)
	if err != nil {
		return Profile{}, err
	}
	enabled, stream := true, true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if input.Stream != nil {
		stream = *input.Stream
	}
	policy := input.RetryPolicy
	if len(policy) == 0 {
		policy = json.RawMessage(`{"schema_version":1}`)
	}
	p := Profile{ID: identity.NewID(), OrganizationID: orgID, Name: strings.TrimSpace(input.Name), Format: input.Format, BaseURL: base, BaseURLHost: host, Model: strings.TrimSpace(input.Model), Priority: input.Priority, Enabled: enabled, Stream: stream, ContextWindow: input.ContextWindow, MaxOutputTokens: input.MaxOutputTokens, RatePerSecond: input.RatePerSecond, RatePerMinute: input.RatePerMinute, ThinkingMode: input.ThinkingMode, ReasoningEffort: input.ReasoningEffort, SessionHeaderName: input.SessionHeaderName, RetryPolicy: policy}
	if p.ThinkingMode == "" {
		p.ThinkingMode = "auto"
	}
	if err := validateProfile(p); err != nil {
		return Profile{}, err
	}
	if input.Credential.Remove || strings.TrimSpace(input.Credential.SecretValue) == "" {
		return Profile{}, ErrInvalid
	}
	secretID := identity.NewID()
	cipherText, nonce, hint, err := s.Secrets.Seal(orgID, secretID, "llm_api_key", input.Credential.SecretValue)
	if err != nil {
		return Profile{}, ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Profile{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO secret_values(id,organization_id,kind,ciphertext,nonce,hint) VALUES($1,$2,'llm_api_key',$3,$4,$5)`, secretID, orgID, cipherText, nonce, hint); err != nil {
		return Profile{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO llm_profiles(id,organization_id,name,format,base_url,base_url_host,model,credential_ref,priority,enabled,stream,context_window,max_output_tokens,rate_per_second,rate_per_minute,thinking_mode,reasoning_effort,session_header_name,retry_policy)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`, p.ID, orgID, p.Name, p.Format, p.BaseURL, p.BaseURLHost, p.Model, secretID, p.Priority, p.Enabled, p.Stream, p.ContextWindow, p.MaxOutputTokens, p.RatePerSecond, p.RatePerMinute, p.ThinkingMode, p.ReasoningEffort, p.SessionHeaderName, p.RetryPolicy); err != nil {
		return Profile{}, err
	}
	if err := profileEvent(ctx, tx, orgID, actorID, p.ID, "llm.profile_created"); err != nil {
		return Profile{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Profile{}, err
	}
	return s.Get(ctx, orgID, p.ID)
}

func profileEvent(ctx context.Context, tx pgx.Tx, orgID, actorID, profileID, kind string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,event_type,payload) VALUES($1,$2,$3,$4)`, identity.NewID(), orgID, kind, map[string]any{"schema_version": 1, "data": map[string]any{"profile_id": profileID}}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,$4,'llm_profile',$5)`, identity.NewID(), orgID, actorID, kind, profileID)
	return err
}

func (s Store) Get(ctx context.Context, orgID, id string) (Profile, error) {
	p, err := scanProfile(s.Pool.QueryRow(ctx, `SELECT `+profileColumns+` FROM llm_profiles p LEFT JOIN secret_values s ON s.id=p.credential_ref WHERE p.organization_id=$1 AND p.id=$2`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	return p, err
}

func (s Store) List(ctx context.Context, orgID string) ([]Profile, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+profileColumns+` FROM llm_profiles p LEFT JOIN secret_values s ON s.id=p.credential_ref WHERE p.organization_id=$1 ORDER BY p.priority DESC,p.name LIMIT 100`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s Store) Update(ctx context.Context, orgID, actorID, id string, input PatchInput) (Profile, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Profile{}, err
	}
	defer tx.Rollback(ctx)
	p, err := scanProfile(tx.QueryRow(ctx, `SELECT `+profileColumns+` FROM llm_profiles p LEFT JOIN secret_values s ON s.id=p.credential_ref WHERE p.organization_id=$1 AND p.id=$2 FOR UPDATE OF p`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	oldHost := p.BaseURLHost
	oldCredential := p.CredentialRef
	if input.Name != nil {
		p.Name = strings.TrimSpace(*input.Name)
	}
	if input.Format != nil {
		p.Format = *input.Format
	}
	if input.BaseURL != nil {
		p.BaseURL, p.BaseURLHost, err = validateBaseURL(*input.BaseURL)
		if err != nil {
			return Profile{}, err
		}
	}
	if input.Model != nil {
		p.Model = strings.TrimSpace(*input.Model)
	}
	if input.Priority != nil {
		p.Priority = *input.Priority
	}
	if input.Enabled != nil {
		p.Enabled = *input.Enabled
	}
	if input.Stream != nil {
		p.Stream = *input.Stream
	}
	if input.ContextWindow != nil {
		p.ContextWindow = *input.ContextWindow
	}
	if input.MaxOutputTokens != nil {
		p.MaxOutputTokens = *input.MaxOutputTokens
	}
	if input.RatePerSecond != nil {
		p.RatePerSecond = *input.RatePerSecond
	}
	if input.RatePerMinute != nil {
		p.RatePerMinute = *input.RatePerMinute
	}
	if input.ThinkingMode != nil {
		p.ThinkingMode = *input.ThinkingMode
	}
	if input.ReasoningEffort != nil {
		p.ReasoningEffort = *input.ReasoningEffort
	}
	if input.SessionHeaderName != nil {
		p.SessionHeaderName = *input.SessionHeaderName
	}
	if len(input.RetryPolicy) > 0 {
		p.RetryPolicy = input.RetryPolicy
	}
	if err := validateProfile(p); err != nil {
		return Profile{}, err
	}
	if p.BaseURLHost != oldHost && (input.Credential == nil || input.Credential.Remove || input.Credential.SecretValue == "") {
		return Profile{}, ErrCredentialRequired
	}
	if input.Credential != nil {
		if input.Credential.Remove && input.Credential.SecretValue != "" {
			return Profile{}, ErrInvalid
		}
		if input.Credential.Remove {
			p.CredentialRef = nil
			p.CredentialHint = nil
			p.Enabled = false
		} else if input.Credential.SecretValue != "" {
			sid := identity.NewID()
			cipherText, nonce, hint, err := s.Secrets.Seal(orgID, sid, "llm_api_key", input.Credential.SecretValue)
			if err != nil {
				return Profile{}, ErrInvalid
			}
			if _, err := tx.Exec(ctx, `INSERT INTO secret_values(id,organization_id,kind,ciphertext,nonce,hint) VALUES($1,$2,'llm_api_key',$3,$4,$5)`, sid, orgID, cipherText, nonce, hint); err != nil {
				return Profile{}, err
			}
			p.CredentialRef = &sid
			p.CredentialHint = &hint
		}
	}
	if p.Enabled && p.CredentialRef == nil {
		return Profile{}, ErrCredentialRequired
	}
	_, err = tx.Exec(ctx, `UPDATE llm_profiles SET name=$3,format=$4,base_url=$5,base_url_host=$6,model=$7,credential_ref=$8,priority=$9,enabled=$10,stream=$11,context_window=$12,max_output_tokens=$13,rate_per_second=$14,rate_per_minute=$15,thinking_mode=$16,reasoning_effort=$17,session_header_name=$18,retry_policy=$19,health_state='healthy',circuit_state='closed',soft_failures=0,cooldown_until=NULL,probe_lease_until=NULL,updated_at=now() WHERE organization_id=$1 AND id=$2`, orgID, id, p.Name, p.Format, p.BaseURL, p.BaseURLHost, p.Model, p.CredentialRef, p.Priority, p.Enabled, p.Stream, p.ContextWindow, p.MaxOutputTokens, p.RatePerSecond, p.RatePerMinute, p.ThinkingMode, p.ReasoningEffort, p.SessionHeaderName, p.RetryPolicy)
	if err != nil {
		return Profile{}, err
	}
	if oldCredential != nil && (p.CredentialRef == nil || *oldCredential != *p.CredentialRef) {
		if _, err := tx.Exec(ctx, `DELETE FROM secret_values WHERE id=$1 AND organization_id=$2`, *oldCredential, orgID); err != nil {
			return Profile{}, err
		}
	}
	if err := profileEvent(ctx, tx, orgID, actorID, id, "llm.profile_updated"); err != nil {
		return Profile{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Profile{}, err
	}
	return s.Get(ctx, orgID, id)
}

func (s Store) Delete(ctx context.Context, orgID, actorID, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var sid *string
	err = tx.QueryRow(ctx, `SELECT credential_ref FROM llm_profiles WHERE id=$1 AND organization_id=$2 FOR UPDATE`, id, orgID).Scan(&sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM llm_profiles WHERE id=$1 AND organization_id=$2`, id, orgID)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23503" {
			return ErrConflict
		}
		return err
	}
	if sid != nil {
		if _, err = tx.Exec(ctx, `DELETE FROM secret_values WHERE id=$1 AND organization_id=$2`, *sid, orgID); err != nil {
			return err
		}
	}
	if err = profileEvent(ctx, tx, orgID, actorID, id, "llm.profile_deleted"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Store) Credential(ctx context.Context, orgID, profileID string) (string, error) {
	var sid string
	var cipherText, nonce []byte
	err := s.Pool.QueryRow(ctx, `SELECT s.id,s.ciphertext,s.nonce FROM llm_profiles p JOIN secret_values s ON s.id=p.credential_ref WHERE p.organization_id=$1 AND p.id=$2 AND p.enabled`, orgID, profileID).Scan(&sid, &cipherText, &nonce)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return s.Secrets.OpenValue(orgID, sid, "llm_api_key", cipherText, nonce)
}
