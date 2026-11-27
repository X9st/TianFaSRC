package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceBrowserRejectsTraversalAndSymlinks(t *testing.T) {
	root := t.TempDir()
	taskID := "00000000-0000-0000-0000-000000000001"
	intentID := "00000000-0000-0000-0000-000000000002"
	dir := filepath.Join(root, "workspaces", taskID, intentID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "secret")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	w := Workspace{Root: root}
	items, err := w.ListTaskFiles(taskID)
	if err != nil || len(items) != 1 || items[0].Name != "note.txt" {
		t.Fatalf("files: %+v %v", items, err)
	}
	content, err := w.ReadTaskFile(taskID, intentID, "note.txt")
	if err != nil || string(content) != "safe" {
		t.Fatalf("read: %q %v", content, err)
	}
	for _, name := range []string{"../secret", "linked.txt", "a\nb", ".hidden"} {
		if _, err := w.ReadTaskFile(taskID, intentID, name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ReadTaskFile(taskID, intentID, "secret"); err == nil {
		t.Fatal("accepted symlinked intent directory")
	}
	if items, err := w.ListTaskFiles(taskID); err != nil || len(items) != 0 {
		t.Fatalf("listed symlinked intent directory: %+v %v", items, err)
	}
	if _, err := w.ReadTaskFile(strings.Repeat("a", 36), intentID, "note.txt"); err == nil {
		t.Fatal("accepted invalid task")
	}
}
