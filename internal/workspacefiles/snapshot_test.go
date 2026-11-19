package workspacefiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotReadsLiteralSymlinkButNeverLinkedDirectoryOrRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("not captured"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	data, mode, exists, err := SnapshotFile(root, "link", 100)
	if err != nil || !exists || mode != "120000" || string(data) != filepath.Join(outside, "secret") {
		t.Fatalf("symlink: %q %s %t %v", data, mode, exists, err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := SnapshotFile(root, "dir/secret", 100); err == nil {
		t.Fatal("followed a symlink directory")
	}
	linkRoot := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(outside, linkRoot); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := SnapshotFile(linkRoot, "secret", 100); err == nil {
		t.Fatal("followed a replaced checkout root")
	}
}
