package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorePersistsAndAuthenticates(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cipherText, nonce, hint, err := first.Seal("org-a", "secret-a", "llm_api_key", "sk-example-1234")
	if err != nil {
		t.Fatal(err)
	}
	if hint != "1234" {
		t.Fatalf("hint %q", hint)
	}
	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := second.OpenValue("org-a", "secret-a", "llm_api_key", cipherText, nonce)
	if err != nil || got != "sk-example-1234" {
		t.Fatalf("decrypt %q %v", got, err)
	}
	if _, err := second.OpenValue("org-b", "secret-a", "llm_api_key", cipherText, nonce); err == nil {
		t.Fatal("cross organization decryption succeeded")
	}
	cipherText[0] ^= 1
	if _, err := second.OpenValue("org-a", "secret-a", "llm_api_key", cipherText, nonce); err == nil {
		t.Fatal("tampering accepted")
	}
	info, err := os.Stat(filepath.Join(dir, "secrets", keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key permissions %v", info.Mode().Perm())
	}
}

func TestStoreRejectsLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secrets", keyFile)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("loose master key accepted")
	}
}
