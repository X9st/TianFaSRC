package finding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/blob"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gocvss31 "github.com/pandatix/go-cvss/31"
)

type Store struct {
	Pool  *pgxpool.Pool
	Blobs blob.Store
}

type Input struct {
	TaskID            string   `json:"task_id"`
	Title             string   `json:"title"`
	VulnerabilityType string   `json:"vulnerability_type"`
	Location          string   `json:"location"`
	Severity          string   `json:"severity"`
	CVSS              *CVSS    `json:"cvss"`
	AssetIDs          []string `json:"asset_ids"`
	Preconditions     string   `json:"preconditions"`
	Impact            string   `json:"impact"`
	ObservedResult    string   `json:"observed_result"`
	Recommendation    string   `json:"recommendation"`
	ReproductionSteps []string `json:"reproduction_steps"`
	SourceIntentID    string   `json:"source_intent_id"`
	SourceRunID       string   `json:"source_run_id"`
}

type CVSS struct {
	Version string  `json:"version"`
	Vector  string  `json:"vector"`
	Score   float64 `json:"score"`
}

type Finding struct {
	ID                    string    `json:"id"`
	TaskID                string    `json:"task_id"`
	NodeID                string    `json:"node_id"`
	Fingerprint           string    `json:"fingerprint"`
	Title                 string    `json:"title"`
	VulnerabilityType     string    `json:"vulnerability_type"`
	Location              string    `json:"location"`
	Severity              string    `json:"severity"`
	CVSS                  *CVSS     `json:"cvss"`
	Status                string    `json:"status"`
	AssetIDs              []string  `json:"asset_ids"`
	Preconditions         string    `json:"preconditions"`
	Impact                string    `json:"impact"`
	ObservedResult        string    `json:"observed_result"`
	Recommendation        string    `json:"recommendation"`
	ReproductionSteps     []string  `json:"reproduction_steps"`
	SourceIntentID        *string   `json:"source_intent_id,omitempty"`
	EvidenceVersion       int64     `json:"evidence_version"`
	ReportEvidenceVersion int64     `json:"report_evidence_version"`
	Version               int64     `json:"version"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func validInput(input Input) bool {
	if strings.TrimSpace(input.Title) == "" || len(input.Title) > 200 || strings.TrimSpace(input.VulnerabilityType) == "" || len(input.VulnerabilityType) > 120 || strings.TrimSpace(input.Location) == "" || len(input.Location) > 2000 || len(input.AssetIDs) == 0 || len(input.AssetIDs) > 50 || len(input.ReproductionSteps) > 50 {
		return false
	}
	switch input.Severity {
	case "info", "low", "medium", "high", "critical":
	default:
		return false
	}
	for _, step := range input.ReproductionSteps {
		if len(step) > 4000 {
			return false
		}
	}
	if input.CVSS != nil {
		if input.CVSS.Version != "3.1" || math.IsNaN(input.CVSS.Score) || math.IsInf(input.CVSS.Score, 0) {
			return false
		}
		parsed, err := gocvss31.ParseVector(input.CVSS.Vector)
		if err != nil || math.Abs(parsed.BaseScore()-input.CVSS.Score) > 0.000001 {
			return false
		}
	}
	return len(input.Preconditions) <= 10000 && len(input.Impact) <= 10000 && len(input.ObservedResult) <= 10000 && len(input.Recommendation) <= 10000
}

func (s Store) Create(ctx context.Context, orgID, actorID string, input Input) (Finding, error) {
	if !validInput(input) {
		return Finding{}, task.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Finding{}, err
	}
	defer tx.Rollback(ctx)
	var originID string
	err = tx.QueryRow(ctx, `SELECT n.id FROM exploration_nodes n JOIN tasks t ON t.id=n.task_id WHERE t.id=$1 AND t.organization_id=$2 AND t.deleted_at IS NULL AND n.kind='origin' FOR UPDATE OF t`, input.TaskID, orgID).Scan(&originID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Finding{}, task.ErrNotFound
	}
	if err != nil {
		return Finding{}, err
	}
	if input.SourceIntentID != "" {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_intents WHERE id=$1 AND task_id=$2)`, input.SourceIntentID, input.TaskID).Scan(&valid); err != nil || !valid {
			return Finding{}, task.ErrInvalid
		}
	}
	if input.SourceRunID != "" {
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=$1 AND task_id=$2 AND organization_id=$3)`, input.SourceRunID, input.TaskID, orgID).Scan(&valid); err != nil || !valid {
			return Finding{}, task.ErrInvalid
		}
	}
	keys := []string{}
	for _, assetID := range input.AssetIDs {
		var key string
		err := tx.QueryRow(ctx, `SELECT a.canonical_key FROM assets a JOIN task_asset_links l ON l.asset_id=a.id WHERE a.id=$1 AND a.organization_id=$2 AND l.task_id=$3`, assetID, orgID, input.TaskID).Scan(&key)
		if errors.Is(err, pgx.ErrNoRows) {
			return Finding{}, task.ErrInvalid
		}
		if err != nil {
			return Finding{}, err
		}
		keys = append(keys, key)
	}
	// The first affected asset is the location anchor; additional assets are
	// retained as relations without changing the deduplication fingerprint.
	fingerprintBytes := sha256.Sum256([]byte(orgID + "\x00" + keys[0] + "\x00" + strings.ToLower(strings.TrimSpace(input.VulnerabilityType)) + "\x00" + strings.TrimSpace(input.Location)))
	fingerprint := "sha256:" + hex.EncodeToString(fingerprintBytes[:])
	var existingID string
	err = tx.QueryRow(ctx, `SELECT id FROM findings WHERE task_id=$1 AND fingerprint=$2`, input.TaskID, fingerprint).Scan(&existingID)
	if err == nil {
		if input.SourceRunID != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO finding_sources(finding_id,run_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, existingID, input.SourceRunID); err != nil {
				return Finding{}, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return Finding{}, err
		}
		return s.Get(ctx, orgID, existingID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Finding{}, err
	}
	id, nodeID := identity.NewID(), identity.NewID()
	steps, _ := json.Marshal(input.ReproductionSteps)
	var cvss any
	if input.CVSS != nil {
		cvss, _ = json.Marshal(input.CVSS)
	}
	var source any
	if input.SourceIntentID != "" {
		source = input.SourceIntentID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO exploration_nodes(id,task_id,kind,state,summary,source_intent_id,run_id) VALUES($1,$2,'finding','draft',$3,$4,$5)`, nodeID, input.TaskID, input.Title, source, optional(input.SourceRunID)); err != nil {
		return Finding{}, err
	}
	fromID := originID
	if input.SourceIntentID != "" {
		fromID = input.SourceIntentID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO exploration_edges(task_id,from_node_id,to_node_id,kind) VALUES($1,$2,$3,'yields')`, input.TaskID, fromID, nodeID); err != nil {
		return Finding{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO findings(id,organization_id,task_id,node_id,fingerprint,title,vulnerability_type,location,severity,cvss,preconditions,impact,reproduction_steps,observed_result,recommendation,source_intent_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, id, orgID, input.TaskID, nodeID, fingerprint, input.Title, input.VulnerabilityType, input.Location, input.Severity, cvss, input.Preconditions, input.Impact, steps, input.ObservedResult, input.Recommendation, source); err != nil {
		return Finding{}, err
	}
	for _, assetID := range input.AssetIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO finding_assets(finding_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, assetID); err != nil {
			return Finding{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, nodeID, assetID); err != nil {
			return Finding{}, err
		}
	}
	if input.SourceRunID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO finding_sources(finding_id,run_id) VALUES($1,$2)`, id, input.SourceRunID); err != nil {
			return Finding{}, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,'finding.created',$4)`, identity.NewID(), orgID, input.TaskID, map[string]any{"finding_id": id}); err != nil {
		return Finding{}, err
	}
	if err := audit(ctx, tx, orgID, actorID, "finding.created", "finding", id, nil); err != nil {
		return Finding{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Finding{}, err
	}
	return s.Get(ctx, orgID, id)
}

func optional(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func audit(ctx context.Context, tx pgx.Tx, orgID, actorID, action, resourceType, resourceID string, detail any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,$4,$5,$6,$7)`, identity.NewID(), orgID, optional(actorID), action, resourceType, resourceID, detail)
	return err
}

func (s Store) Get(ctx context.Context, orgID, id string) (Finding, error) {
	var item Finding
	var steps []byte
	var cvss []byte
	err := s.Pool.QueryRow(ctx, `SELECT id,task_id,node_id,fingerprint,title,vulnerability_type,location,severity,cvss,status,preconditions,impact,reproduction_steps,observed_result,recommendation,source_intent_id,evidence_version,report_evidence_version,version,created_at,updated_at FROM findings WHERE id=$1 AND organization_id=$2`, id, orgID).Scan(&item.ID, &item.TaskID, &item.NodeID, &item.Fingerprint, &item.Title, &item.VulnerabilityType, &item.Location, &item.Severity, &cvss, &item.Status, &item.Preconditions, &item.Impact, &steps, &item.ObservedResult, &item.Recommendation, &item.SourceIntentID, &item.EvidenceVersion, &item.ReportEvidenceVersion, &item.Version, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Finding{}, task.ErrNotFound
	}
	if err != nil {
		return Finding{}, err
	}
	_ = json.Unmarshal(steps, &item.ReproductionSteps)
	if len(cvss) > 0 {
		if err := json.Unmarshal(cvss, &item.CVSS); err != nil {
			return Finding{}, err
		}
	}
	rows, err := s.Pool.Query(ctx, `SELECT asset_id FROM finding_assets WHERE finding_id=$1 ORDER BY asset_id`, id)
	if err != nil {
		return Finding{}, err
	}
	defer rows.Close()
	item.AssetIDs = []string{}
	for rows.Next() {
		var assetID string
		if err := rows.Scan(&assetID); err != nil {
			return Finding{}, err
		}
		item.AssetIDs = append(item.AssetIDs, assetID)
	}
	return item, rows.Err()
}

func (s Store) List(ctx context.Context, orgID, taskID string) ([]Finding, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM findings WHERE organization_id=$1 AND ($2='' OR task_id::text=$2) ORDER BY created_at DESC,id DESC LIMIT 200`, orgID, taskID)
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
	items := []Finding{}
	for _, id := range ids {
		item, err := s.Get(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

var ErrVersionConflict = errors.New("证据版本已变化，请重新读取")

type Evidence struct {
	BindingID       string              `json:"binding_id"`
	FindingID       string              `json:"finding_id"`
	SnapshotID      string              `json:"snapshot_id"`
	Role            string              `json:"role"`
	Note            string              `json:"note"`
	Position        int                 `json:"position"`
	CapturedAt      time.Time           `json:"captured_at"`
	RequestSHA256   *string             `json:"request_sha256,omitempty"`
	ResponseSHA256  *string             `json:"response_sha256,omitempty"`
	Metadata        json.RawMessage     `json:"metadata"`
	RequestHeaders  map[string][]string `json:"request_headers"`
	ResponseHeaders map[string][]string `json:"response_headers"`
}

func (s Store) BindTraffic(ctx context.Context, orgID, actorID, findingID, trafficID, role, note string) (Evidence, error) {
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
	var taskID string
	if err := tx.QueryRow(ctx, `SELECT task_id FROM findings WHERE id=$1 AND organization_id=$2 FOR UPDATE`, findingID, orgID).Scan(&taskID); errors.Is(err, pgx.ErrNoRows) {
		return Evidence{}, task.ErrNotFound
	} else if err != nil {
		return Evidence{}, err
	}
	var snapshotID string
	err = tx.QueryRow(ctx, `SELECT id FROM evidence_snapshots WHERE task_id=$1 AND source_traffic_id=$2`, taskID, trafficID).Scan(&snapshotID)
	if errors.Is(err, pgx.ErrNoRows) {
		var metadata, requestHeaders, responseHeaders []byte
		var requestHash, responseHash *string
		var capturedAt time.Time
		err = tx.QueryRow(ctx, `SELECT jsonb_build_object('method',method,'url',url,'status_code',status_code,'remote_ip',remote_ip,'tls',tls,'request_bytes',request_bytes,'response_bytes',response_bytes,'request_body_bytes',request_body_bytes,'response_body_bytes',response_body_bytes,'request_body_sha256',request_body_sha256,'response_body_sha256',response_body_sha256,'requested_at',requested_at,'responded_at',responded_at),request_headers,response_headers,request_sha256,response_sha256,responded_at FROM traffic WHERE id=$1 AND organization_id=$2 AND task_id=$3 FOR SHARE`, trafficID, orgID, taskID).Scan(&metadata, &requestHeaders, &responseHeaders, &requestHash, &responseHash, &capturedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return Evidence{}, task.ErrNotFound
		}
		if err != nil {
			return Evidence{}, err
		}
		for _, hash := range []*string{requestHash, responseHash} {
			if hash != nil {
				if _, err := s.Blobs.Read(ctx, *hash); err != nil {
					return Evidence{}, err
				}
			}
		}
		snapshotID = identity.NewID()
		if _, err := tx.Exec(ctx, `INSERT INTO evidence_snapshots(id,organization_id,task_id,source_traffic_id,metadata,request_headers,response_headers,request_sha256,response_sha256,captured_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, snapshotID, orgID, taskID, trafficID, metadata, requestHeaders, responseHeaders, requestHash, responseHash, capturedAt); err != nil {
			return Evidence{}, err
		}
		for _, hash := range []*string{requestHash, responseHash} {
			if hash != nil {
				if _, err := tx.Exec(ctx, `UPDATE blobs SET evidence_refs=evidence_refs+1,orphaned_at=NULL WHERE sha256=$1`, *hash); err != nil {
					return Evidence{}, err
				}
			}
		}
	} else if err != nil {
		return Evidence{}, err
	}
	var existingID string
	err = tx.QueryRow(ctx, `SELECT id FROM evidence_bindings WHERE finding_id=$1 AND snapshot_id=$2`, findingID, snapshotID).Scan(&existingID)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return Evidence{}, err
		}
		return s.evidenceByID(ctx, orgID, existingID, false)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Evidence{}, err
	}
	var next int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(position),-1)+1 FROM evidence_bindings WHERE finding_id=$1`, findingID).Scan(&next); err != nil {
		return Evidence{}, err
	}
	bindingID := identity.NewID()
	if _, err := tx.Exec(ctx, `INSERT INTO evidence_bindings(id,finding_id,snapshot_id,role,note,position) VALUES($1,$2,$3,$4,$5,$6)`, bindingID, findingID, snapshotID, role, note, next); err != nil {
		return Evidence{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE findings SET evidence_version=evidence_version+1,version=version+1,updated_at=now() WHERE id=$1`, findingID); err != nil {
		return Evidence{}, err
	}
	if err := audit(ctx, tx, orgID, actorID, "evidence.bound", "finding", findingID, map[string]any{"binding_id": bindingID, "traffic_id": trafficID}); err != nil {
		return Evidence{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,'evidence.bound',$4)`, identity.NewID(), orgID, taskID, map[string]any{"finding_id": findingID, "binding_id": bindingID}); err != nil {
		return Evidence{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Evidence{}, err
	}
	return s.evidenceByID(ctx, orgID, bindingID, false)
}

func (s Store) evidenceByID(ctx context.Context, orgID, id string, reveal bool) (Evidence, error) {
	var item Evidence
	var requestHeaders, responseHeaders []byte
	err := s.Pool.QueryRow(ctx, `SELECT b.id,b.finding_id,b.snapshot_id,b.role,b.note,b.position,s.captured_at,s.request_sha256,s.response_sha256,s.metadata,s.request_headers,s.response_headers FROM evidence_bindings b JOIN evidence_snapshots s ON s.id=b.snapshot_id WHERE b.id=$1 AND s.organization_id=$2`, id, orgID).Scan(&item.BindingID, &item.FindingID, &item.SnapshotID, &item.Role, &item.Note, &item.Position, &item.CapturedAt, &item.RequestSHA256, &item.ResponseSHA256, &item.Metadata, &requestHeaders, &responseHeaders)
	if errors.Is(err, pgx.ErrNoRows) {
		return Evidence{}, task.ErrNotFound
	}
	if err != nil {
		return Evidence{}, err
	}
	_ = json.Unmarshal(requestHeaders, &item.RequestHeaders)
	_ = json.Unmarshal(responseHeaders, &item.ResponseHeaders)
	if !reveal {
		item.RequestHeaders = redact(item.RequestHeaders)
		item.ResponseHeaders = redact(item.ResponseHeaders)
	}
	return item, nil
}

func redact(headers map[string][]string) map[string][]string {
	copy := map[string][]string{}
	for key, values := range headers {
		switch strings.ToLower(key) {
		case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key":
			copy[key] = []string{"••••"}
		default:
			copy[key] = append([]string{}, values...)
		}
	}
	return copy
}

func (s Store) Evidence(ctx context.Context, orgID, findingID string) ([]Evidence, error) {
	rows, err := s.Pool.Query(ctx, `SELECT b.id FROM evidence_bindings b JOIN findings f ON f.id=b.finding_id WHERE b.finding_id=$1 AND f.organization_id=$2 ORDER BY b.position,b.id`, findingID, orgID)
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
	items := []Evidence{}
	for _, id := range ids {
		item, err := s.evidenceByID(ctx, orgID, id, false)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s Store) ReadEvidence(ctx context.Context, orgID, bindingID, part string) ([]byte, error) {
	item, err := s.evidenceByID(ctx, orgID, bindingID, true)
	if err != nil {
		return nil, err
	}
	if part == "request" && item.RequestSHA256 != nil {
		return s.Blobs.Read(ctx, *item.RequestSHA256)
	}
	if part == "response" && item.ResponseSHA256 != nil {
		return s.Blobs.Read(ctx, *item.ResponseSHA256)
	}
	return nil, task.ErrInvalid
}

func (s Store) Unbind(ctx context.Context, orgID, actorID, bindingID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var findingID, taskID, snapshotID string
	err = tx.QueryRow(ctx, `SELECT f.id,f.task_id,b.snapshot_id FROM evidence_bindings b JOIN findings f ON f.id=b.finding_id WHERE b.id=$1 AND f.organization_id=$2 FOR UPDATE OF f`, bindingID, orgID).Scan(&findingID, &taskID, &snapshotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return task.ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM evidence_bindings WHERE id=$1`, bindingID); err != nil {
		return err
	}
	var stillUsed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM evidence_bindings WHERE snapshot_id=$1) OR EXISTS(SELECT 1 FROM retest_evidence WHERE snapshot_id=$1)`, snapshotID).Scan(&stillUsed); err != nil {
		return err
	}
	if !stillUsed {
		var requestHash, responseHash *string
		if err := tx.QueryRow(ctx, `DELETE FROM evidence_snapshots WHERE id=$1 RETURNING request_sha256,response_sha256`, snapshotID).Scan(&requestHash, &responseHash); err != nil {
			return err
		}
		for _, hash := range []*string{requestHash, responseHash} {
			if hash != nil {
				if _, err := tx.Exec(ctx, `UPDATE blobs SET evidence_refs=evidence_refs-1,orphaned_at=CASE WHEN evidence_refs=1 AND traffic_refs=0 THEN now() ELSE orphaned_at END WHERE sha256=$1`, *hash); err != nil {
					return err
				}
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE findings SET evidence_version=evidence_version+1,version=version+1,updated_at=now() WHERE id=$1`, findingID); err != nil {
		return err
	}
	if err := audit(ctx, tx, orgID, actorID, "evidence.unbound", "finding", findingID, map[string]any{"binding_id": bindingID}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,'evidence.unbound',$4)`, identity.NewID(), orgID, taskID, map[string]any{"finding_id": findingID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Store) Confirm(ctx context.Context, orgID, actorID, id string, version int64) (Finding, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Finding{}, err
	}
	defer tx.Rollback(ctx)
	var current, evidenceVersion int64
	var status, taskID, nodeID string
	err = tx.QueryRow(ctx, `SELECT version,evidence_version,status,task_id,node_id FROM findings WHERE id=$1 AND organization_id=$2 FOR UPDATE`, id, orgID).Scan(&current, &evidenceVersion, &status, &taskID, &nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Finding{}, task.ErrNotFound
	}
	if err != nil {
		return Finding{}, err
	}
	if current != version {
		return Finding{}, ErrVersionConflict
	}
	if status != "draft" {
		return Finding{}, task.ErrConflict
	}
	var proofs int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM evidence_bindings WHERE finding_id=$1 AND role='proof'`, id).Scan(&proofs); err != nil {
		return Finding{}, err
	}
	if proofs == 0 {
		return Finding{}, fmt.Errorf("%w: 缺少 proof 证据", task.ErrInvalid)
	}
	if _, err := tx.Exec(ctx, `UPDATE findings SET status='confirmed',version=version+1,updated_at=now() WHERE id=$1`, id); err != nil {
		return Finding{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE exploration_nodes SET state='confirmed',version=version+1,updated_at=now() WHERE id=$1`, nodeID); err != nil {
		return Finding{}, err
	}
	if err := audit(ctx, tx, orgID, actorID, "finding.confirmed", "finding", id, map[string]any{"evidence_version": evidenceVersion}); err != nil {
		return Finding{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO domain_events(id,organization_id,task_id,event_type,payload) VALUES($1,$2,$3,'finding.confirmed',$4)`, identity.NewID(), orgID, taskID, map[string]any{"finding_id": id}); err != nil {
		return Finding{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Finding{}, err
	}
	return s.Get(ctx, orgID, id)
}
