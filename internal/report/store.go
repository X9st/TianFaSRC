package report

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/blob"
	"github.com/X9st/TianFaSRC/internal/finding"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool  *pgxpool.Pool
	Blobs blob.Store
}

type Report struct {
	TaskID                 string    `json:"task_id"`
	Body                   string    `json:"body"`
	Version                int64     `json:"version"`
	EvidenceVersion        int64     `json:"evidence_version"`
	CurrentEvidenceVersion int64     `json:"current_evidence_version"`
	Stale                  bool      `json:"stale"`
	UpdatedAt              time.Time `json:"updated_at"`
}

var ErrVersionConflict = errors.New("报告或证据版本已变化，请重新读取")

func (s Store) evidenceVersion(ctx context.Context, orgID, taskID string) (int64, error) {
	var version int64
	err := s.Pool.QueryRow(ctx, `SELECT coalesce(sum(f.evidence_version+1),0) FROM findings f JOIN tasks t ON t.id=f.task_id WHERE t.id=$1 AND t.organization_id=$2`, taskID, orgID).Scan(&version)
	return version, err
}

func (s Store) Get(ctx context.Context, orgID, taskID string) (Report, error) {
	var exists bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL)`, taskID, orgID).Scan(&exists); err != nil {
		return Report{}, err
	}
	if !exists {
		return Report{}, task.ErrNotFound
	}
	current, err := s.evidenceVersion(ctx, orgID, taskID)
	if err != nil {
		return Report{}, err
	}
	item := Report{TaskID: taskID, Version: 0, CurrentEvidenceVersion: current}
	err = s.Pool.QueryRow(ctx, `SELECT body,version,evidence_version,updated_at FROM task_reports WHERE task_id=$1 AND organization_id=$2`, taskID, orgID).Scan(&item.Body, &item.Version, &item.EvidenceVersion, &item.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		item.Stale = current != 0
		return item, nil
	}
	if err != nil {
		return Report{}, err
	}
	item.Stale = item.EvidenceVersion != current
	return item, nil
}

func (s Store) Update(ctx context.Context, orgID, actorID, taskID, body string, version, expectedEvidenceVersion int64) (Report, error) {
	if len(body) > 50000 {
		return Report{}, task.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback(ctx)
	var lockedTask string
	err = tx.QueryRow(ctx, `SELECT id FROM tasks WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL FOR SHARE`, taskID, orgID).Scan(&lockedTask)
	if errors.Is(err, pgx.ErrNoRows) {
		return Report{}, task.ErrNotFound
	}
	if err != nil {
		return Report{}, err
	}
	// Binding mutations lock the finding rows, so this snapshot cannot change
	// between the version check and report commit.
	rows, err := tx.Query(ctx, `SELECT evidence_version FROM findings WHERE task_id=$1 ORDER BY id FOR SHARE`, taskID)
	if err != nil {
		return Report{}, err
	}
	currentEvidence := int64(0)
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return Report{}, err
		}
		currentEvidence += v + 1
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Report{}, err
	}
	rows.Close()
	if currentEvidence != expectedEvidenceVersion {
		return Report{}, ErrVersionConflict
	}
	var currentVersion int64
	err = tx.QueryRow(ctx, `SELECT version FROM task_reports WHERE task_id=$1 FOR UPDATE`, taskID).Scan(&currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		currentVersion = 0
	} else if err != nil {
		return Report{}, err
	}
	if currentVersion != version {
		return Report{}, ErrVersionConflict
	}
	if currentVersion == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO task_reports(task_id,organization_id,body,version,evidence_version,updated_by) VALUES($1,$2,$3,1,$4,$5)`, taskID, orgID, body, currentEvidence, actorID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE task_reports SET body=$3,version=version+1,evidence_version=$4,updated_by=$5,updated_at=now() WHERE task_id=$1 AND organization_id=$2`, taskID, orgID, body, currentEvidence, actorID)
	}
	if err != nil {
		return Report{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE findings SET report_evidence_version=evidence_version WHERE task_id=$1`, taskID); err != nil {
		return Report{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'report.updated','task',$4,$5)`, identity.NewID(), orgID, actorID, taskID, map[string]any{"evidence_version": currentEvidence}); err != nil {
		return Report{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Report{}, err
	}
	return s.Get(ctx, orgID, taskID)
}

type exportedFinding struct {
	Finding  finding.Finding    `json:"finding"`
	Evidence []finding.Evidence `json:"evidence"`
	Retests  []finding.Retest   `json:"retests"`
}

type Document struct {
	TaskID               string            `json:"task_id"`
	Title                string            `json:"title"`
	GeneratedAt          time.Time         `json:"generated_at"`
	EvidenceVersion      int64             `json:"evidence_version"`
	SavedEvidenceVersion int64             `json:"saved_evidence_version"`
	Stale                bool              `json:"stale"`
	Findings             []exportedFinding `json:"findings"`
}

func (s Store) document(ctx context.Context, orgID, taskID string) (Document, error) {
	var title string
	err := s.Pool.QueryRow(ctx, `SELECT title FROM tasks WHERE id=$1 AND organization_id=$2 AND deleted_at IS NULL`, taskID, orgID).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return Document{}, task.ErrNotFound
	}
	if err != nil {
		return Document{}, err
	}
	findings, err := (finding.Store{Pool: s.Pool, Blobs: s.Blobs}).List(ctx, orgID, taskID)
	if err != nil {
		return Document{}, err
	}
	doc := Document{TaskID: taskID, Title: title, GeneratedAt: time.Now().UTC(), Findings: []exportedFinding{}}
	for _, item := range findings {
		evidence, err := (finding.Store{Pool: s.Pool, Blobs: s.Blobs}).Evidence(ctx, orgID, item.ID)
		if err != nil {
			return Document{}, err
		}
		for _, proof := range evidence {
			for _, hash := range []*string{proof.RequestSHA256, proof.ResponseSHA256} {
				if hash != nil {
					if _, err := s.Blobs.Read(ctx, *hash); err != nil {
						return Document{}, err
					}
				}
			}
		}
		retests, err := (finding.Store{Pool: s.Pool, Blobs: s.Blobs}).ListRetests(ctx, orgID, item.ID)
		if err != nil {
			return Document{}, err
		}
		doc.EvidenceVersion += item.EvidenceVersion + 1
		doc.Findings = append(doc.Findings, exportedFinding{Finding: item, Evidence: evidence, Retests: retests})
	}
	return doc, nil
}

func markdown(doc Document, intro string) []byte {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n生成时间：%s  ·  证据版本：%d\n\n", doc.Title, doc.GeneratedAt.Format(time.RFC3339), doc.EvidenceVersion)
	if doc.Stale {
		fmt.Fprintf(&out, "**注意：已保存报告基于证据版本 %d，当前证据版本为 %d；需重新审查。**\n\n", doc.SavedEvidenceVersion, doc.EvidenceVersion)
	}
	if strings.TrimSpace(intro) != "" {
		out.WriteString(intro)
		out.WriteString("\n\n")
	}
	for _, item := range doc.Findings {
		f := item.Finding
		fmt.Fprintf(&out, "## %s\n\n- 严重级别：%s\n- 类型：%s\n- 位置：%s\n- 状态：%s\n- 指纹：%s\n\n", f.Title, f.Severity, f.VulnerabilityType, f.Location, f.Status, f.Fingerprint)
		if f.CVSS != nil {
			fmt.Fprintf(&out, "- CVSS %s：%.1f（%s）\n\n", f.CVSS.Version, f.CVSS.Score, f.CVSS.Vector)
		}
		fmt.Fprintf(&out, "**前置条件**：%s\n\n**影响**：%s\n\n**观察结果**：%s\n\n**修复建议**：%s\n\n", f.Preconditions, f.Impact, f.ObservedResult, f.Recommendation)
		if len(f.ReproductionSteps) > 0 {
			out.WriteString("复现步骤：\n\n")
			for i, step := range f.ReproductionSteps {
				fmt.Fprintf(&out, "%d. %s\n", i+1, step)
			}
			out.WriteString("\n")
		}
		out.WriteString("证据：\n\n")
		for index, e := range item.Evidence {
			fmt.Fprintf(&out, "%d. [%s] %s（%s）\n   - 请求 SHA-256：%s\n   - 响应 SHA-256：%s\n", index+1, e.Role, e.Note, e.CapturedAt.Format(time.RFC3339), hashValue(e.RequestSHA256), hashValue(e.ResponseSHA256))
		}
		if len(item.Retests) > 0 {
			out.WriteString("\n复测历史：\n\n")
			for _, retest := range item.Retests {
				result := "待完成"
				if retest.Conclusion != nil {
					result = *retest.Conclusion
				}
				fmt.Fprintf(&out, "- %s · %s · 固定证据版本 %d · %s\n", retest.CreatedAt.Format(time.RFC3339), result, retest.EvidenceVersion, retest.Note)
			}
		}
		out.WriteString("\n")
	}
	return []byte(out.String())
}
func hashValue(value *string) string {
	if value == nil {
		return "无"
	}
	return *value
}

func (s Store) Export(ctx context.Context, orgID, taskID, format string) ([]byte, string, error) {
	if format != "markdown" && format != "json" && format != "md-zip" {
		return nil, "", task.ErrInvalid
	}
	doc, err := s.document(ctx, orgID, taskID)
	if err != nil {
		return nil, "", err
	}
	report, err := s.Get(ctx, orgID, taskID)
	if err != nil {
		return nil, "", err
	}
	if doc.EvidenceVersion != report.CurrentEvidenceVersion {
		return nil, "", ErrVersionConflict
	}
	doc.SavedEvidenceVersion = report.EvidenceVersion
	doc.Stale = report.Stale
	checkCurrent := func() error {
		latest, err := s.evidenceVersion(ctx, orgID, taskID)
		if err != nil {
			return err
		}
		if latest != doc.EvidenceVersion {
			return ErrVersionConflict
		}
		return nil
	}
	if format == "json" {
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, "", err
		}
		if err := checkCurrent(); err != nil {
			return nil, "", err
		}
		return data, "application/json", nil
	}
	md := markdown(doc, report.Body)
	if format == "markdown" {
		if err := checkCurrent(); err != nil {
			return nil, "", err
		}
		return md, "text/markdown; charset=utf-8", nil
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	add := func(name string, data []byte) error {
		f, err := writer.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	if err := add("report.md", md); err != nil {
		return nil, "", err
	}
	manifest := map[string]any{"task_id": taskID, "evidence_version": doc.EvidenceVersion, "generated_at": doc.GeneratedAt, "files": map[string]string{}}
	files := manifest["files"].(map[string]string)
	seen := map[string]bool{}
	for _, item := range doc.Findings {
		for _, proof := range item.Evidence {
			for _, part := range []struct {
				name string
				hash *string
			}{{"request", proof.RequestSHA256}, {"response", proof.ResponseSHA256}} {
				if part.hash == nil {
					continue
				}
				name := "evidence/" + proof.SnapshotID + "/" + part.name + ".bin"
				if seen[name] {
					continue
				}
				data, err := s.Blobs.Read(ctx, *part.hash)
				if err != nil {
					return nil, "", err
				}
				if err := add(name, data); err != nil {
					return nil, "", err
				}
				files[name] = *part.hash
				seen[name] = true
			}
		}
	}
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	if err := add("manifest.json", manifestBytes); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	// Verify the generated archive before returning it to a client.
	reader, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		return nil, "", err
	}
	for _, file := range reader.File {
		stream, err := file.Open()
		if err != nil {
			return nil, "", err
		}
		_, err = io.Copy(io.Discard, stream)
		_ = stream.Close()
		if err != nil {
			return nil, "", err
		}
	}
	if err := checkCurrent(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), "application/zip", nil
}
