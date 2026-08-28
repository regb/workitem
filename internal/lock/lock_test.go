package lock_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/lock"
)

func TestTryAcquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "item.lock")
	first, err := lock.TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := lock.TryAcquire(path)
	if err == nil {
		second.Release()
		t.Fatal("second lock unexpectedly succeeded")
	}
	if !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("second lock error = %v, want ErrLocked", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	third, err := lock.TryAcquire(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatal(err)
	}
}
