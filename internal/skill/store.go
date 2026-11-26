package skill

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

type Manifest struct {
	Name          string   `yaml:"name" json:"name"`
	Version       string   `yaml:"version" json:"version"`
	Description   string   `yaml:"description" json:"description"`
	Compatibility string   `yaml:"compatibility" json:"compatibility,omitempty"`
	Agents        []string `yaml:"agents" json:"agents"`
	MCPs          []string `yaml:"mcps" json:"mcps"`
	Capabilities  []string `yaml:"capabilities" json:"capabilities"`
}

type Record struct {
	ID          string    `json:"id"`
	Manifest    Manifest  `json:"manifest"`
	ContentHash string    `json:"content_hash"`
	Enabled     bool      `json:"enabled"`
	Source      string    `json:"source"`
	InstalledAt time.Time `json:"installed_at"`
}

type Store struct{ Pool *pgxpool.Pool }

func ValidateZIP(data []byte) (Manifest, string, string, error) {
	if len(data) == 0 || len(data) > 4<<20 {
		return Manifest{}, "", "", task.ErrInvalid
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) == 0 || len(reader.File) > 100 {
		return Manifest{}, "", "", task.ErrInvalid
	}
	var text string
	var total uint64
	for _, file := range reader.File {
		name := file.Name
		if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.ContainsRune(name, 0) || path.Clean(name) != name || name == "." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") || len(name) > 240 {
			return Manifest{}, "", "", task.ErrInvalid
		}
		if !file.Mode().IsRegular() && !file.FileInfo().IsDir() {
			return Manifest{}, "", "", task.ErrInvalid
		}
		if file.UncompressedSize64 > 512<<10 || (file.CompressedSize64 > 0 && file.UncompressedSize64 > file.CompressedSize64*100) {
			return Manifest{}, "", "", task.ErrInvalid
		}
		total += file.UncompressedSize64
		if total > 4<<20 {
			return Manifest{}, "", "", task.ErrInvalid
		}
		if name == "SKILL.md" {
			if file.UncompressedSize64 > 64<<10 {
				return Manifest{}, "", "", task.ErrInvalid
			}
			stream, err := file.Open()
			if err != nil {
				return Manifest{}, "", "", err
			}
			body, err := io.ReadAll(io.LimitReader(stream, 64<<10+1))
			stream.Close()
			if err != nil {
				return Manifest{}, "", "", err
			}
			text = string(body)
		}
	}
	parts := strings.SplitN(text, "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		return Manifest{}, "", "", errors.New("SKILL.md 必须以 YAML frontmatter 开头")
	}
	var manifest Manifest
	decoder := yaml.NewDecoder(strings.NewReader(parts[1]))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, "", "", err
	}
	if len(manifest.Name) == 0 || len(manifest.Name) > 80 || len(manifest.Version) == 0 || len(manifest.Version) > 80 || len(manifest.Description) == 0 || len(manifest.Description) > 1000 || len(parts[2]) == 0 {
		return Manifest{}, "", "", task.ErrInvalid
	}
	if manifest.Agents == nil {
		manifest.Agents = []string{}
	}
	if manifest.MCPs == nil {
		manifest.MCPs = []string{}
	}
	if manifest.Capabilities == nil {
		manifest.Capabilities = []string{}
	}
	for _, char := range manifest.Name {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return Manifest{}, "", "", task.ErrInvalid
		}
	}
	for _, role := range manifest.Agents {
		if role != "planner" && role != "worker" && role != "decomposer" {
			return Manifest{}, "", "", task.ErrInvalid
		}
	}
	for _, capability := range manifest.Capabilities {
		switch capability {
		case "network.http", "workspace.read", "workspace.write", "command.run", "mcp.call":
		default:
			return Manifest{}, "", "", task.ErrInvalid
		}
	}
	for _, server := range manifest.MCPs {
		if len(server) == 0 || len(server) > 80 || strings.Trim(server, "abcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
			return Manifest{}, "", "", task.ErrInvalid
		}
	}
	sum := sha256.Sum256(data)
	return manifest, text, hex.EncodeToString(sum[:]), nil
}

func (s Store) InstallZIP(ctx context.Context, orgID, actorID string, data []byte) (Record, error) {
	manifest, body, hash, err := ValidateZIP(data)
	if err != nil {
		return Record{}, err
	}
	id := identity.NewID()
	var installed time.Time
	err = s.Pool.QueryRow(ctx, `INSERT INTO skills(id,organization_id,name,version,description,body,content_hash,source,agents,capabilities,mcps,installed_by) VALUES($1,$2,$3,$4,$5,$6,$7,'zip',$8,$9,$10,$11) ON CONFLICT(organization_id,name,version) DO UPDATE SET enabled=true WHERE skills.content_hash=excluded.content_hash RETURNING id,installed_at`, id, orgID, manifest.Name, manifest.Version, manifest.Description, body, hash, manifest.Agents, manifest.Capabilities, manifest.MCPs, actorID).Scan(&id, &installed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, task.ErrConflict
	}
	if err != nil {
		return Record{}, err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'skill.installed','skill',$4,$5)`, identity.NewID(), orgID, actorID, id, map[string]any{"name": manifest.Name, "version": manifest.Version, "hash": hash})
	if err != nil {
		return Record{}, err
	}
	return Record{ID: id, Manifest: manifest, ContentHash: hash, Enabled: true, Source: "zip", InstalledAt: installed}, nil
}

func (s Store) List(ctx context.Context, orgID string) ([]Record, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,version,description,agents,capabilities,mcps,content_hash,enabled,source,installed_at FROM skills WHERE organization_id=$1 ORDER BY name,installed_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Record{}
	for rows.Next() {
		var item Record
		if err := rows.Scan(&item.ID, &item.Manifest.Name, &item.Manifest.Version, &item.Manifest.Description, &item.Manifest.Agents, &item.Manifest.Capabilities, &item.Manifest.MCPs, &item.ContentHash, &item.Enabled, &item.Source, &item.InstalledAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Store) Load(ctx context.Context, orgID, runID, name string) (Record, string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Record{}, "", err
	}
	defer tx.Rollback(ctx)
	var record Record
	var body string
	err = tx.QueryRow(ctx, `SELECT s.id,s.name,s.version,s.description,s.agents,s.capabilities,s.mcps,s.content_hash,s.enabled,s.source,s.installed_at,s.body FROM skills s JOIN agent_runs r ON r.organization_id=s.organization_id AND r.id=$2 WHERE s.organization_id=$1 AND s.name=$3 AND s.enabled AND r.role=ANY(s.agents) ORDER BY s.installed_at DESC LIMIT 1`, orgID, runID, name).Scan(&record.ID, &record.Manifest.Name, &record.Manifest.Version, &record.Manifest.Description, &record.Manifest.Agents, &record.Manifest.Capabilities, &record.Manifest.MCPs, &record.ContentHash, &record.Enabled, &record.Source, &record.InstalledAt, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, "", task.ErrNotFound
	}
	if err != nil {
		return Record{}, "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO run_loaded_skills(run_id,skill_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, runID, record.ID); err != nil {
		return Record{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return Record{}, "", err
	}
	return record, body, nil
}

func (s Store) SetEnabled(ctx context.Context, orgID, actorID, id string, enabled bool) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE skills SET enabled=$3 WHERE id=$1 AND organization_id=$2`, id, orgID, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return task.ErrNotFound
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'skill.enabled_changed','skill',$4,$5)`, identity.NewID(), orgID, actorID, id, map[string]any{"enabled": enabled})
	return err
}

func (m Manifest) String() string { return fmt.Sprintf("%s@%s: %s", m.Name, m.Version, m.Description) }
