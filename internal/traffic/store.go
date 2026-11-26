package traffic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/blob"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool  *pgxpool.Pool
	Blobs blob.Store
}

type RecordInput struct {
	OrganizationID, TaskID, RunID, IntentID, ToolCallID string
	Method, URL, RemoteIP                               string
	StatusCode                                          int
	TLS                                                 map[string]any
	RequestHeaders, ResponseHeaders                     map[string][]string
	RequestRaw, ResponseRaw                             []byte
	RequestedAt, RespondedAt                            time.Time
}

type Entry struct {
	ID                 string              `json:"id"`
	TaskID             string              `json:"task_id"`
	RunID              *string             `json:"run_id,omitempty"`
	IntentID           *string             `json:"intent_id,omitempty"`
	ToolCallID         *string             `json:"tool_call_id,omitempty"`
	Method             string              `json:"method"`
	URL                string              `json:"url"`
	Host               string              `json:"host"`
	Path               string              `json:"path"`
	StatusCode         int                 `json:"status_code"`
	RemoteIP           string              `json:"remote_ip"`
	TLS                json.RawMessage     `json:"tls"`
	RequestHeaders     map[string][]string `json:"request_headers"`
	ResponseHeaders    map[string][]string `json:"response_headers"`
	RequestSHA256      *string             `json:"request_sha256,omitempty"`
	ResponseSHA256     *string             `json:"response_sha256,omitempty"`
	RequestBytes       int64               `json:"request_bytes"`
	ResponseBytes      int64               `json:"response_bytes"`
	RequestBodyBytes   int64               `json:"request_body_bytes"`
	ResponseBodyBytes  int64               `json:"response_body_bytes"`
	RequestBodySHA256  string              `json:"request_body_sha256"`
	ResponseBodySHA256 string              `json:"response_body_sha256"`
	RequestedAt        time.Time           `json:"requested_at"`
	RespondedAt        time.Time           `json:"responded_at"`
}

func optional(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func bodyMetadata(raw []byte) (int64, string) {
	marker := []byte("\r\n\r\n")
	index := bytes.Index(raw, marker)
	if index < 0 {
		return 0, ""
	}
	body := raw[index+len(marker):]
	hash := sha256.Sum256(body)
	return int64(len(body)), hex.EncodeToString(hash[:])
}

func (s Store) Record(ctx context.Context, input RecordInput) (Entry, error) {
	u, err := url.Parse(input.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || input.StatusCode < 100 || input.StatusCode > 599 || input.RemoteIP == "" || input.RequestedAt.IsZero() || input.RespondedAt.Before(input.RequestedAt) {
		return Entry{}, task.ErrInvalid
	}
	requestHash, err := s.Blobs.Put(ctx, input.RequestRaw)
	if err != nil {
		return Entry{}, err
	}
	responseHash, err := s.Blobs.Put(ctx, input.ResponseRaw)
	if err != nil {
		return Entry{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback(ctx)
	for _, item := range []struct {
		hash string
		size int
	}{{requestHash, len(input.RequestRaw)}, {responseHash, len(input.ResponseRaw)}} {
		if _, err := tx.Exec(ctx, `INSERT INTO blobs(sha256,size_bytes,traffic_refs) VALUES($1,$2,1) ON CONFLICT(sha256) DO UPDATE SET traffic_refs=blobs.traffic_refs+1,orphaned_at=NULL WHERE blobs.size_bytes=excluded.size_bytes`, item.hash, item.size); err != nil {
			return Entry{}, err
		}
	}
	// The collector may have removed an orphan between the first Put and the
	// reference update. Recheck while the blob rows are locked by this tx.
	for _, raw := range [][]byte{input.RequestRaw, input.ResponseRaw} {
		if _, err := s.Blobs.Put(ctx, raw); err != nil {
			return Entry{}, err
		}
	}
	requestHeaders, _ := json.Marshal(input.RequestHeaders)
	responseHeaders, _ := json.Marshal(input.ResponseHeaders)
	tls, _ := json.Marshal(input.TLS)
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	requestBodyBytes, requestBodySHA := bodyMetadata(input.RequestRaw)
	responseBodyBytes, responseBodySHA := bodyMetadata(input.ResponseRaw)
	entry := Entry{ID: identity.NewID(), TaskID: input.TaskID, Method: input.Method, URL: input.URL, Host: u.Hostname(), Path: path, StatusCode: input.StatusCode, RemoteIP: input.RemoteIP, TLS: tls, RequestHeaders: input.RequestHeaders, ResponseHeaders: input.ResponseHeaders, RequestSHA256: &requestHash, ResponseSHA256: &responseHash, RequestBytes: int64(len(input.RequestRaw)), ResponseBytes: int64(len(input.ResponseRaw)), RequestBodyBytes: requestBodyBytes, ResponseBodyBytes: responseBodyBytes, RequestBodySHA256: requestBodySHA, ResponseBodySHA256: responseBodySHA, RequestedAt: input.RequestedAt, RespondedAt: input.RespondedAt}
	_, err = tx.Exec(ctx, `INSERT INTO traffic(id,organization_id,task_id,run_id,intent_id,tool_call_id,method,url,host,path,status_code,remote_ip,tls,request_headers,response_headers,request_sha256,response_sha256,request_bytes,response_bytes,request_body_bytes,response_body_bytes,request_body_sha256,response_body_sha256,requested_at,responded_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)`, entry.ID, input.OrganizationID, input.TaskID, optional(input.RunID), optional(input.IntentID), optional(input.ToolCallID), entry.Method, entry.URL, entry.Host, entry.Path, entry.StatusCode, entry.RemoteIP, tls, requestHeaders, responseHeaders, requestHash, responseHash, entry.RequestBytes, entry.ResponseBytes, entry.RequestBodyBytes, entry.ResponseBodyBytes, entry.RequestBodySHA256, entry.ResponseBodySHA256, entry.RequestedAt, entry.RespondedAt)
	if err != nil {
		return Entry{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,'traffic.recorded',$4)`, identity.NewID(), input.OrganizationID, input.TaskID, map[string]any{"traffic_id": entry.ID, "tool_call_id": input.ToolCallID}); err != nil {
		return Entry{}, err
	}
	return entry, tx.Commit(ctx)
}

func mask(headers map[string][]string) map[string][]string {
	copy := make(map[string][]string, len(headers))
	for key, values := range headers {
		lower := strings.ToLower(key)
		if lower == "authorization" || lower == "proxy-authorization" || lower == "cookie" || lower == "set-cookie" || lower == "x-api-key" || lower == "api-key" {
			copy[key] = []string{"••••"}
		} else {
			copy[key] = append([]string{}, values...)
		}
	}
	return copy
}

func (s Store) Get(ctx context.Context, orgID, id string, reveal bool) (Entry, error) {
	var entry Entry
	var requestHeaders, responseHeaders []byte
	err := s.Pool.QueryRow(ctx, `SELECT id,task_id,run_id,intent_id,tool_call_id,method,url,host,path,status_code,remote_ip,tls,request_headers,response_headers,request_sha256,response_sha256,request_bytes,response_bytes,request_body_bytes,response_body_bytes,request_body_sha256,response_body_sha256,requested_at,responded_at FROM traffic WHERE id=$1 AND organization_id=$2`, id, orgID).Scan(&entry.ID, &entry.TaskID, &entry.RunID, &entry.IntentID, &entry.ToolCallID, &entry.Method, &entry.URL, &entry.Host, &entry.Path, &entry.StatusCode, &entry.RemoteIP, &entry.TLS, &requestHeaders, &responseHeaders, &entry.RequestSHA256, &entry.ResponseSHA256, &entry.RequestBytes, &entry.ResponseBytes, &entry.RequestBodyBytes, &entry.ResponseBodyBytes, &entry.RequestBodySHA256, &entry.ResponseBodySHA256, &entry.RequestedAt, &entry.RespondedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, task.ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	_ = json.Unmarshal(requestHeaders, &entry.RequestHeaders)
	_ = json.Unmarshal(responseHeaders, &entry.ResponseHeaders)
	if !reveal {
		entry.RequestHeaders = mask(entry.RequestHeaders)
		entry.ResponseHeaders = mask(entry.ResponseHeaders)
	}
	return entry, nil
}

type Filter struct {
	TaskID, Host, Path, BodyKeyword    string
	StatusCode                         int
	After, Before                      *time.Time
	MinResponseBytes, MaxResponseBytes int64
	Cursor                             string
	Limit                              int
}

func (s Store) List(ctx context.Context, orgID string, filter Filter) ([]Entry, error) {
	if filter.Limit < 1 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.Cursor == "" {
		filter.Cursor = "ffffffff-ffff-7fff-bfff-ffffffffffff"
	}
	rows, err := s.Pool.Query(ctx, `SELECT id FROM traffic WHERE organization_id=$1 AND ($2='' OR task_id::text=$2) AND ($3='' OR host=$3) AND ($4='' OR path ILIKE '%'||$4||'%') AND ($5=0 OR status_code=$5) AND ($6::timestamptz IS NULL OR requested_at>=$6) AND ($7::timestamptz IS NULL OR requested_at<=$7) AND ($8=0 OR response_body_bytes>=$8) AND ($9=0 OR response_body_bytes<=$9) AND id<$10 ORDER BY id DESC LIMIT $11`, orgID, filter.TaskID, filter.Host, filter.Path, filter.StatusCode, filter.After, filter.Before, filter.MinResponseBytes, filter.MaxResponseBytes, filter.Cursor, filter.Limit)
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
	result := []Entry{}
	for _, id := range ids {
		entry, err := s.Get(ctx, orgID, id, false)
		if err != nil {
			return nil, err
		}
		if filter.BodyKeyword != "" {
			data, err := s.Blobs.Read(ctx, *entry.ResponseSHA256)
			if err != nil {
				return nil, err
			}
			_, body, hasBody := strings.Cut(string(data), "\r\n\r\n")
			if !hasBody || !strings.Contains(strings.ToLower(body), strings.ToLower(filter.BodyKeyword)) {
				continue
			}
		}
		result = append(result, entry)
	}
	return result, nil
}

func (s Store) ReadBody(ctx context.Context, orgID, id, part string) ([]byte, error) {
	entry, err := s.Get(ctx, orgID, id, true)
	if err != nil {
		return nil, err
	}
	if part == "request" && entry.RequestSHA256 != nil {
		return s.Blobs.Read(ctx, *entry.RequestSHA256)
	}
	if part == "response" && entry.ResponseSHA256 != nil {
		return s.Blobs.Read(ctx, *entry.ResponseSHA256)
	}
	return nil, task.ErrInvalid
}

func (s Store) Delete(ctx context.Context, orgID, id string, actorID ...string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var requestHash, responseHash *string
	err = tx.QueryRow(ctx, `DELETE FROM traffic WHERE id=$1 AND organization_id=$2 RETURNING request_sha256,response_sha256`, id, orgID).Scan(&requestHash, &responseHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return task.ErrNotFound
	}
	if err != nil {
		return err
	}
	for _, hash := range []*string{requestHash, responseHash} {
		if hash == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE blobs SET traffic_refs=traffic_refs-1,orphaned_at=CASE WHEN traffic_refs=1 AND evidence_refs=0 THEN now() ELSE orphaned_at END WHERE sha256=$1`, *hash); err != nil {
			return err
		}
	}
	if len(actorID) > 0 && actorID[0] != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'traffic.deleted','traffic',$4)`, identity.NewID(), orgID, actorID[0], id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Collect removes files only after a grace period and only when both ref domains
// are empty. It also finds file-only orphans left by failed transactions.
func (s Store) Collect(ctx context.Context, before time.Time) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT sha256 FROM blobs WHERE traffic_refs=0 AND evidence_refs=0 AND orphaned_at<$1 LIMIT 500`, before)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	hashes := []string{}
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return count, err
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	rows.Close()
	for _, hash := range hashes {
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return count, err
		}
		var candidate string
		err = tx.QueryRow(ctx, `SELECT sha256 FROM blobs WHERE sha256=$1 AND traffic_refs=0 AND evidence_refs=0 AND orphaned_at<$2 FOR UPDATE`, hash, before).Scan(&candidate)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			continue
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return count, err
		}
		if err := s.Blobs.Remove(candidate); err != nil {
			_ = tx.Rollback(ctx)
			return count, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM blobs WHERE sha256=$1`, candidate); err != nil {
			_ = tx.Rollback(ctx)
			return count, err
		}
		if err := tx.Commit(ctx); err != nil {
			return count, err
		}
		count++
	}
	root := filepath.Join(s.Blobs.Root, "blobs")
	directories, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return count, nil
	}
	if err != nil {
		return count, err
	}
	for _, directory := range directories {
		if !directory.IsDir() || len(directory.Name()) != 2 {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, directory.Name()))
		if err != nil {
			return count, err
		}
		for _, file := range files {
			if err := ctx.Err(); err != nil {
				return count, err
			}
			if !file.Type().IsRegular() || len(file.Name()) != 64 || !strings.HasPrefix(file.Name(), directory.Name()) {
				continue
			}
			info, err := file.Info()
			if err != nil {
				return count, err
			}
			if !info.ModTime().Before(before) {
				continue
			}
			var exists bool
			if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blobs WHERE sha256=$1)`, file.Name()).Scan(&exists); err != nil {
				return count, err
			}
			if exists {
				continue
			}
			if err := s.Blobs.Remove(file.Name()); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}
