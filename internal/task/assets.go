package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/X9st/TianFaSRC/internal/identity"
)

func canonicalAsset(kind, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrInvalid
	}
	switch kind {
	case "company":
		return strings.ToLower(value), nil
	case "root_domain", "subdomain":
		if _, err := netip.ParseAddr(value); err == nil {
			return "", ErrInvalid
		}
		return normalizeHost(value)
	case "ip":
		address, err := netip.ParseAddr(value)
		if err != nil {
			return "", err
		}
		return address.Unmap().String(), nil
	case "service":
		normalized, err := NormalizeScope(ScopeEntryInput{Effect: "allow", Kind: "service", Value: value})
		return normalized.NormalizedValue, err
	case "application", "endpoint":
		u, err := url.Parse(value)
		if err != nil || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || strings.Contains(value, "\\") {
			return "", ErrInvalid
		}
		host, err := normalizeHost(u.Hostname())
		if err != nil {
			return "", err
		}
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		path := u.EscapedPath()
		if path == "" {
			path = "/"
		}
		lowerPath := strings.ToLower(path)
		for _, encoded := range []string{"%2f", "%5c", "%25", "%2e", "%00"} {
			if strings.Contains(lowerPath, encoded) {
				return "", ErrInvalid
			}
		}
		if kind == "application" {
			path = "/"
		}
		return fmt.Sprintf("%s://%s:%s%s", u.Scheme, host, port, path), nil
	default:
		return "", ErrInvalid
	}
}

func (s Store) UpsertAsset(ctx context.Context, orgID, actorID string, input AssetInput) (Asset, error) {
	key, err := canonicalAsset(input.Kind, input.Value)
	if err != nil {
		return Asset{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if input.Confidence == 0 {
		input.Confidence = 1
	}
	if input.Confidence < 0 || input.Confidence > 1 {
		return Asset{}, ErrInvalid
	}
	if len(input.Attributes) == 0 {
		input.Attributes = json.RawMessage(`{"schema_version":1}`)
	}
	var attrs map[string]any
	if json.Unmarshal(input.Attributes, &attrs) != nil || attrs["schema_version"] != float64(1) {
		return Asset{}, ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Asset{}, err
	}
	defer tx.Rollback(ctx)
	if input.ParentID != nil {
		var parentKind string
		if err := tx.QueryRow(ctx, "SELECT kind FROM assets WHERE id=$1 AND organization_id=$2", *input.ParentID, orgID).Scan(&parentKind); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Asset{}, ErrInvalid
			}
			return Asset{}, err
		}
		expected := map[string]string{"root_domain": "company", "subdomain": "root_domain", "ip": "subdomain", "service": "ip", "application": "service", "endpoint": "application"}[input.Kind]
		if expected == "" || parentKind != expected {
			return Asset{}, ErrInvalid
		}
	}
	if input.SourceTaskID != nil {
		var valid bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL)", *input.SourceTaskID, orgID).Scan(&valid); err != nil {
			return Asset{}, err
		}
		if !valid {
			return Asset{}, ErrInvalid
		}
	}
	var asset Asset
	err = tx.QueryRow(ctx, `INSERT INTO assets(id,organization_id,kind,canonical_key,display_value,raw_value,parent_id,attributes_json,source_task_id,confidence)
		VALUES($1,$2,$3,$4,$5,$5,$6,$7,$8,$9)
		ON CONFLICT(organization_id,kind,canonical_key) DO UPDATE SET
		last_seen_at=now(),updated_at=now(),
		attributes_json=assets.attributes_json || EXCLUDED.attributes_json,
		confidence=greatest(assets.confidence,EXCLUDED.confidence)
		RETURNING id,organization_id,kind,canonical_key,display_value,raw_value,parent_id,attributes_json,source_task_id,confidence,first_seen_at,last_seen_at`,
		identity.NewID(), orgID, input.Kind, key, input.Value, input.ParentID, input.Attributes, input.SourceTaskID, input.Confidence).Scan(
		&asset.ID, &asset.OrganizationID, &asset.Kind, &asset.CanonicalKey, &asset.DisplayValue, &asset.RawValue, &asset.ParentID, &asset.Attributes, &asset.SourceTaskID, &asset.Confidence, &asset.FirstSeenAt, &asset.LastSeenAt)
	if err != nil {
		return Asset{}, err
	}
	if input.SourceTaskID != nil {
		if _, err := tx.Exec(ctx, "INSERT INTO task_asset_links(task_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING", *input.SourceTaskID, asset.ID); err != nil {
			return Asset{}, err
		}
		var originID string
		if err := tx.QueryRow(ctx, "SELECT id FROM exploration_nodes WHERE task_id=$1 AND kind='origin'", *input.SourceTaskID).Scan(&originID); err != nil {
			return Asset{}, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING", originID, asset.ID); err != nil {
			return Asset{}, err
		}
	}
	eventTaskID := ""
	if input.SourceTaskID != nil {
		eventTaskID = *input.SourceTaskID
	}
	if err := emit(ctx, tx, orgID, eventTaskID, "asset.upserted", map[string]any{"asset_id": asset.ID, "kind": asset.Kind}); err != nil {
		return Asset{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id)
		VALUES($1,$2,$3,'asset.upserted','asset',$4)`, identity.NewID(), orgID, actorID, asset.ID); err != nil {
		return Asset{}, err
	}
	return asset, tx.Commit(ctx)
}

func (s Store) ListAssets(ctx context.Context, orgID, kind, cursor string, limit int) ([]Asset, error) {
	if limit < 1 || limit > 10000 {
		limit = 30
	}
	if cursor == "" {
		cursor = "ffffffff-ffff-7fff-bfff-ffffffffffff"
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,organization_id,kind,canonical_key,display_value,raw_value,parent_id,attributes_json,source_task_id,confidence,first_seen_at,last_seen_at
		FROM assets WHERE organization_id=$1 AND ($2='' OR kind=$2) AND id<$3 ORDER BY id DESC LIMIT $4`, orgID, kind, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Asset{}
	for rows.Next() {
		var asset Asset
		if err := rows.Scan(&asset.ID, &asset.OrganizationID, &asset.Kind, &asset.CanonicalKey, &asset.DisplayValue, &asset.RawValue, &asset.ParentID, &asset.Attributes, &asset.SourceTaskID, &asset.Confidence, &asset.FirstSeenAt, &asset.LastSeenAt); err != nil {
			return nil, err
		}
		result = append(result, asset)
	}
	return result, rows.Err()
}

func (s Store) LinkAsset(ctx context.Context, orgID, actorID, taskID, assetID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var originID string
	err = tx.QueryRow(ctx, `SELECT n.id FROM exploration_nodes n JOIN tasks t ON t.id=n.task_id
		WHERE t.id=$1 AND t.organization_id=$2 AND t.deleted_at IS NULL AND n.kind='origin'`, taskID, orgID).Scan(&originID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM assets WHERE id=$1 AND organization_id=$2)", assetID, orgID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrInvalid
	}
	if _, err := tx.Exec(ctx, "INSERT INTO task_asset_links(task_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING", taskID, assetID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING", originID, assetID); err != nil {
		return err
	}
	if err := emit(ctx, tx, orgID, taskID, "asset.linked", map[string]any{"asset_id": assetID}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'asset.linked','asset',$4)`, identity.NewID(), orgID, actorID, assetID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type AssetGraph struct {
	Nodes []Asset `json:"nodes"`
	Edges []struct {
		FromAssetID string `json:"from_asset_id"`
		ToAssetID   string `json:"to_asset_id"`
	} `json:"edges"`
}

func (s Store) GraphAssets(ctx context.Context, orgID string) (AssetGraph, error) {
	nodes, err := s.ListAssets(ctx, orgID, "", "", 10000)
	if err != nil {
		return AssetGraph{}, err
	}
	graph := AssetGraph{Nodes: nodes, Edges: []struct {
		FromAssetID string `json:"from_asset_id"`
		ToAssetID   string `json:"to_asset_id"`
	}{}}
	for _, node := range nodes {
		if node.ParentID != nil {
			graph.Edges = append(graph.Edges, struct {
				FromAssetID string `json:"from_asset_id"`
				ToAssetID   string `json:"to_asset_id"`
			}{*node.ParentID, node.ID})
		}
	}
	return graph, nil
}

func (s Store) AnchorAsset(ctx context.Context, orgID, actorID, nodeID, assetID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var taskID string
	err = tx.QueryRow(ctx, `SELECT n.task_id FROM exploration_nodes n JOIN tasks t ON t.id=n.task_id WHERE n.id=$1 AND t.organization_id=$2 AND t.deleted_at IS NULL`, nodeID, orgID).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assets WHERE id=$1 AND organization_id=$2)`, assetID, orgID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrInvalid
	}
	if _, err := tx.Exec(ctx, `INSERT INTO task_asset_links(task_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, taskID, assetID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, nodeID, assetID); err != nil {
		return err
	}
	if err := emit(ctx, tx, orgID, taskID, "asset.anchored", map[string]any{"asset_id": assetID, "node_id": nodeID}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'asset.anchored','asset',$4)`, identity.NewID(), orgID, actorID, assetID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
