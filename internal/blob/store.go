package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrCorrupt = errors.New("证据文件缺失或哈希不匹配")

type Store struct{ Root string }

func validHash(hash string) bool {
	return len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == ""
}

func (s Store) path(hash string) (string, error) {
	if !validHash(hash) || s.Root == "" {
		return "", errors.New("无效 Blob 标识或数据目录")
	}
	return filepath.Join(s.Root, "blobs", hash[:2], hash), nil
}

// Put is safe to call before the referring database transaction. A failed
// transaction leaves an unreferenced file for the orphan collector.
func (s Store) Put(ctx context.Context, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	path, err := s.path(hash)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		_, err = s.Read(ctx, hash)
		return hash, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return "", err
	}
	if err := ctx.Err(); err != nil {
		file.Close()
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	// Link never replaces an existing content-addressed file.
	if err := os.Link(file.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	_, err = s.Read(ctx, hash)
	return hash, err
}

func (s Store) Read(ctx context.Context, hash string) ([]byte, error) {
	path, err := s.path(hash)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrCorrupt
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, fmt.Errorf("%w: %s", ErrCorrupt, hash)
	}
	return data, nil
}

func (s Store) Remove(hash string) error {
	path, err := s.path(hash)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
