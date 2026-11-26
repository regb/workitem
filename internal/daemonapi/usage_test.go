package daemonapi_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/daemonapi"
)

// fakeUsage stands in for the usage plugin: each check reports its number,
// and a check can be held open until released.
type fakeUsage struct {
	mu       sync.Mutex
	runs     int
	refresh  []bool
	hold     chan struct{}
	failNext bool
}

func (u *fakeUsage) run(ctx context.Context, refresh bool) (*v1.GetUsageResponse, error) {
	u.mu.Lock()
	u.runs++
	n := u.runs
	u.refresh = append(u.refresh, refresh)
	hold, fail := u.hold, u.failNext
	u.failNext = false
	u.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if fail {
		return nil, errors.New("the usage plugin failed: provider unreachable")
	}
	return &v1.GetUsageResponse{Enabled: true, Quotas: []*v1.UsageQuota{{Provider: "claude", Account: string(rune('0' + n))}}}, nil
}

func (u *fakeUsage) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.runs
}

func TestUsageAnswersFromTheLastCheckAndRefreshesInTheBackground(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	fake := &fakeUsage{}
	f.API().SetUsageCheck(fake.run)
	get := func(refresh, wait bool) *v1.GetUsageResponse {
		t.Helper()
		value, err := f.Clients.System.GetUsage(ctx, connect.NewRequest(&v1.GetUsageRequest{Refresh: refresh, Wait: wait}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg
	}
	// The first call has nothing to show, so it waits for a check.
	first := get(false, false)
	if !first.Enabled || len(first.Quotas) != 1 || first.Quotas[0].Account != "1" || first.CheckedAt == nil || first.Refreshing {
		t.Fatalf("first: %v", first)
	}
	// Later calls answer from it without checking again.
	if again := get(false, false); again.Quotas[0].Account != "1" || fake.count() != 1 {
		t.Fatalf("cached: %v runs=%d", again, fake.count())
	}
	// A refresh answers at once and reports the check running.
	fake.mu.Lock()
	fake.hold = make(chan struct{})
	fake.mu.Unlock()
	started := get(true, false)
	if !started.Refreshing || started.Quotas[0].Account != "1" {
		t.Fatalf("refresh started: %v", started)
	}
	close(fake.hold)
	fake.mu.Lock()
	fake.hold = nil
	fake.mu.Unlock()
	waited := get(false, false)
	for deadline := time.Now().Add(5 * time.Second); waited.Refreshing && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		waited = get(false, false)
	}
	if waited.Refreshing || waited.Quotas[0].Account != "2" || !fake.refresh[1] {
		t.Fatalf("refreshed: %v refresh=%v", waited, fake.refresh)
	}
	// A failed check keeps the previous quotas and says why.
	fake.mu.Lock()
	fake.failNext = true
	fake.mu.Unlock()
	failed := get(true, true)
	if failed.Error == "" || len(failed.Quotas) != 1 || failed.Quotas[0].Account != "2" {
		t.Fatalf("failed check: %v", failed)
	}
	if next := get(true, true); next.Error != "" || next.Quotas[0].Account != "4" {
		t.Fatalf("recovered: %v", next)
	}
}

func TestBackgroundUsageChecksRunOnlyWhileTheAPIIsInUse(t *testing.T) {
	f := apitest.New(t)
	fake := &fakeUsage{}
	s := f.API()
	s.SetUsageCheck(fake.run)
	wait := func() {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if !s.UsageChecking() {
				return
			}
		}
		t.Fatal("check did not finish")
	}
	now := time.Now()
	// Nobody has used the API: nothing runs.
	s.SetLastActivity(now.Add(-daemonapi.UsageIdle - time.Minute))
	s.MaybeCheckUsage(now)
	wait()
	if fake.count() != 0 {
		t.Fatalf("idle daemon checked usage %d times", fake.count())
	}
	// In use, with no result yet: a check runs.
	s.SetLastActivity(now)
	s.MaybeCheckUsage(now)
	wait()
	if fake.count() != 1 || fake.refresh[0] {
		t.Fatalf("active check: runs=%d refresh=%v", fake.count(), fake.refresh)
	}
	// A result younger than five minutes is kept.
	s.MaybeCheckUsage(time.Now().Add(daemonapi.UsageInterval - time.Minute))
	wait()
	if fake.count() != 1 {
		t.Fatalf("fresh result checked again: %d", fake.count())
	}
	// An older one is renewed while the API stays in use.
	s.SetLastActivity(time.Now().Add(daemonapi.UsageInterval))
	s.MaybeCheckUsage(time.Now().Add(daemonapi.UsageInterval + time.Minute))
	wait()
	if fake.count() != 2 {
		t.Fatalf("stale result not renewed: %d", fake.count())
	}
}

func TestAnOldUsageResultIsReadAgain(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	fake := &fakeUsage{}
	f.API().SetUsageCheck(fake.run)
	get := func() *v1.GetUsageResponse {
		t.Helper()
		value, err := f.Clients.System.GetUsage(ctx, connect.NewRequest(&v1.GetUsageRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg
	}
	get()
	// A result older than daemonapi.UsageFresh is read again from the plugin, whose
	// own cache answers, so the page shows what `wi usage` shows.
	f.API().SetUsageResult(nil, time.Now().Add(-daemonapi.UsageFresh-time.Second))
	if again := get(); again.Quotas[0].Account != "2" || fake.count() != 2 || fake.refresh[1] {
		t.Fatalf("old result reused: %v runs=%d refresh=%v", again, fake.count(), fake.refresh)
	}
}
