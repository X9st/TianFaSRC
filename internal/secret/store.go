package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const keyFile = "master.key"

func Exists(dataDir string) bool {
	info, err := os.Lstat(filepath.Join(dataDir, "secrets", keyFile))
	return err == nil && info.Mode().IsRegular()
}

type Store struct{ key [32]byte }

// Open creates a process-shared data-directory key once. The file is never
// overwritten: a missing key on an existing installation must be restored
// from backup rather than silently replacing it.
func Open(dataDir string) (Store, error) {
	dir := filepath.Join(dataDir, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Store{}, fmt.Errorf("create secret directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return Store{}, errors.New("secret directory must be a private directory")
	}
	path := filepath.Join(dir, keyFile)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		var key [32]byte
		if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
			return Store{}, err
		}
		temp, err := os.CreateTemp(dir, ".master-*")
		if err != nil {
			return Store{}, err
		}
		name := temp.Name()
		defer os.Remove(name)
		if err := temp.Chmod(0600); err != nil {
			temp.Close()
			return Store{}, err
		}
		if _, err := temp.Write(key[:]); err != nil {
			temp.Close()
			return Store{}, err
		}
		if err := temp.Sync(); err != nil {
			temp.Close()
			return Store{}, err
		}
		if err := temp.Close(); err != nil {
			return Store{}, err
		}
		if err := os.Link(name, path); err != nil && !errors.Is(err, os.ErrExist) {
			return Store{}, err
		}
	}
	info, err = os.Lstat(path)
	if err != nil {
		return Store{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return Store{}, errors.New("master key must be a private regular file")
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return Store{}, err
	}
	if len(bytes) != 32 {
		return Store{}, errors.New("invalid master key length")
	}
	var out Store
	copy(out.key[:], bytes)
	return out, nil
}

func (s Store) Seal(orgID, secretID, kind, value string) ([]byte, []byte, string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 16384 {
		return nil, nil, "", errors.New("credential must be 1..16384 bytes")
	}
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return nil, nil, "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, "", err
	}
	cipherText := aead.Seal(nil, nonce, []byte(value), aad(orgID, secretID, kind))
	hint := value
	if len(hint) > 4 {
		hint = hint[len(hint)-4:]
	}
	return cipherText, nonce, hint, nil
}

func (s Store) OpenValue(orgID, secretID, kind string, cipherText, nonce []byte) (string, error) {
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := aead.Open(nil, nonce, cipherText, aad(orgID, secretID, kind))
	if err != nil {
		return "", errors.New("credential decryption failed")
	}
	return string(plain), nil
}

func aad(orgID, secretID, kind string) []byte { return []byte(orgID + "/" + secretID + "/" + kind) }
