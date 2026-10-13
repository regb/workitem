package lock

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocksRejectSymlinkTargets(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "lock")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if guard, err := Acquire(context.Background(), path); err == nil {
		guard.Release()
		t.Fatal("acquired symlink lock")
	}
	if guard, err := TryAcquire(path); err == nil {
		guard.Release()
		t.Fatal("acquired symlink lock")
	}
	if data, _ := os.ReadFile(target); string(data) != "keep" {
		t.Fatal("target modified")
	}
}
