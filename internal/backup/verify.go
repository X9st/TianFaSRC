package backup

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/X9st/TianFaSRC/internal/secret"
)

type Report struct {
	BlobsVerified int   `json:"blobs_verified"`
	TrafficRefs   int64 `json:"traffic_refs"`
	EvidenceRefs  int64 `json:"evidence_refs"`
	OrphanRows    int   `json:"orphan_rows"`
	Secrets       int   `json:"secrets"`
}

// Verify checks every database blob reference against its file and verifies
// that the two reference domains match traffic and immutable evidence rows.
// Run this against a stopped installation or an isolated restored copy.
func Verify(ctx context.Context, pool *pgxpool.Pool, dataDir string) (Report, error) {
	if pool == nil || dataDir == "" {
		return Report{}, errors.New("database and data directory are required")
	}
	var report Report
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM secret_values").Scan(&report.Secrets); err != nil {
		return report, err
	}
	if report.Secrets > 0 {
		if err := requirePrivateFile(filepath.Join(dataDir, "secrets", "master.key")); err != nil {
			return report, fmt.Errorf("encrypted credentials require master key: %w", err)
		}
		secrets, err := secret.Open(dataDir)
		if err != nil {
			return report, fmt.Errorf("open backup master key: %w", err)
		}
		rows, err := pool.Query(ctx, "SELECT id,organization_id,kind,ciphertext,nonce,key_version FROM secret_values")
		if err != nil {
			return report, err
		}
		for rows.Next() {
			var id, orgID, kind string
			var ciphertext, nonce []byte
			var keyVersion int
			if err := rows.Scan(&id, &orgID, &kind, &ciphertext, &nonce, &keyVersion); err != nil {
				rows.Close()
				return report, err
			}
			if keyVersion != 1 {
				rows.Close()
				return report, fmt.Errorf("secret %s uses unsupported key version %d", id, keyVersion)
			}
			if _, err := secrets.OpenValue(orgID, id, kind, ciphertext, nonce); err != nil {
				rows.Close()
				return report, fmt.Errorf("secret %s cannot be decrypted: %w", id, err)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return report, err
		}
		rows.Close()
	}
	if info, err := os.Lstat(filepath.Join(dataDir, "secrets", "master.key")); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != 32 {
			return report, errors.New("master key is not a private 32-byte regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return report, err
	}
	certPath, keyPath := filepath.Join(dataDir, "ca", "ca.crt"), filepath.Join(dataDir, "ca", "ca.key")
	certInfo, certErr := os.Lstat(certPath)
	keyInfo, keyErr := os.Lstat(keyPath)
	if (certErr == nil) != (keyErr == nil) {
		return report, errors.New("recording CA certificate and key must be restored together")
	}
	if certErr == nil {
		if !certInfo.Mode().IsRegular() || !keyInfo.Mode().IsRegular() || keyInfo.Mode().Perm()&0077 != 0 {
			return report, errors.New("recording CA files are not private regular files")
		}
		certPEM, err := os.ReadFile(certPath)
		if err != nil {
			return report, err
		}
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return report, err
		}
		if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
			return report, fmt.Errorf("recording CA certificate and key do not match: %w", err)
		}
	} else if !errors.Is(certErr, os.ErrNotExist) || !errors.Is(keyErr, os.ErrNotExist) {
		return report, errors.Join(certErr, keyErr)
	}

	traffic, err := referenceCounts(ctx, pool, "SELECT hash,count(*) FROM (SELECT request_sha256 AS hash FROM traffic WHERE request_sha256 IS NOT NULL UNION ALL SELECT response_sha256 AS hash FROM traffic WHERE response_sha256 IS NOT NULL) refs GROUP BY hash")
	if err != nil {
		return report, fmt.Errorf("traffic references: %w", err)
	}
	evidence, err := referenceCounts(ctx, pool, "SELECT hash,count(*) FROM (SELECT request_sha256 AS hash FROM evidence_snapshots WHERE request_sha256 IS NOT NULL UNION ALL SELECT response_sha256 AS hash FROM evidence_snapshots WHERE response_sha256 IS NOT NULL) refs GROUP BY hash")
	if err != nil {
		return report, fmt.Errorf("evidence references: %w", err)
	}
	rows, err := pool.Query(ctx, "SELECT sha256,size_bytes,traffic_refs,evidence_refs FROM blobs ORDER BY sha256")
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		var size, trafficRefs, evidenceRefs int64
		if err := rows.Scan(&hash, &size, &trafficRefs, &evidenceRefs); err != nil {
			return report, err
		}
		if trafficRefs != traffic[hash] || evidenceRefs != evidence[hash] {
			return report, fmt.Errorf("blob %s reference mismatch: database traffic=%d evidence=%d, actual traffic=%d evidence=%d", hash, trafficRefs, evidenceRefs, traffic[hash], evidence[hash])
		}
		delete(traffic, hash)
		delete(evidence, hash)
		if trafficRefs+evidenceRefs == 0 {
			report.OrphanRows++
			continue
		}
		if err := verifyBlob(ctx, dataDir, hash, size); err != nil {
			return report, err
		}
		report.BlobsVerified++
		report.TrafficRefs += trafficRefs
		report.EvidenceRefs += evidenceRefs
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	if len(traffic) > 0 || len(evidence) > 0 {
		return report, errors.New("traffic or evidence points to a blob without a database row")
	}
	return report, nil
}

func referenceCounts(ctx context.Context, pool *pgxpool.Pool, query string) (map[string]int64, error) {
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var hash string
		var count int64
		if err := rows.Scan(&hash, &count); err != nil {
			return nil, err
		}
		counts[hash] = count
	}
	return counts, rows.Err()
}

func requirePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("file is missing, linked, or readable by other users")
	}
	return nil
}

func verifyBlob(ctx context.Context, dataDir, hash string, expectedSize int64) error {
	if len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
		return fmt.Errorf("invalid blob hash %q", hash)
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	relative := filepath.Join("blobs", hash[:2], hash)
	info, err := root.Lstat(relative)
	if err != nil {
		return fmt.Errorf("blob %s is missing: %w", hash, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("blob %s is not a regular file", hash)
	}
	file, err := root.Open(relative)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != expectedSize {
		return fmt.Errorf("blob %s changed or has wrong size", hash)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != hash {
		return fmt.Errorf("blob %s failed SHA-256 verification", hash)
	}
	return nil
}
