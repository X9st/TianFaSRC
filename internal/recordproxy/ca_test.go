package recordproxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCARejectsLoosePrivateKeyAndDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := OpenCA(root); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "ca", "ca.key")
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCA(root); err == nil {
		t.Fatal("loose private key accepted")
	}
	if err := os.Chmod(key, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "ca"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCA(root); err == nil {
		t.Fatal("loose CA directory accepted")
	}
}
