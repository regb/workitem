package lock_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

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

func TestSharedLeasesAllowConcurrentToolsAndFenceRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claim.lock")
	first, err := lock.AcquireShared(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := lock.AcquireShared(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if exclusive, err := lock.Acquire(ctx, path); err == nil {
		_ = exclusive.Release()
		t.Fatal("exclusive claim release acquired while tool leases were active")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exclusive acquire error=%v", err)
	}
}
