package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeProvider struct {
	calls        atomic.Int32
	now          func() time.Time
	state        State
	retry        time.Duration
	allowed      *bool
	preventStale bool
}

func (p *fakeProvider) Account() Account { return Account{"fixture", "local"} }
func (p *fakeProvider) Quota(context.Context) Quota {
	p.calls.Add(1)
	time.Sleep(5 * time.Millisecond)
	now := p.now()
	q := Quota{Account: p.Account(), State: p.state, CheckedAt: now, Windows: []Window{}, IncludedUsageAllowed: p.allowed, PreventStaleFallback: p.preventStale}
	if p.state == OK {
		q.ObservedAt = &now
		q.Windows = []Window{{ID: "five_hour", RemainingPercent: floatPtr(88)}}
	}
	if p.retry > 0 {
		next := now.Add(p.retry)
		q.NextRefreshAt = &next
	}
	return q
}
func TestCacheConcurrencyCooldownAndStale(t *testing.T) {
	now := testNow
	c := Cache{Root: t.TempDir(), Now: func() time.Time { return now }}
	p := &fakeProvider{now: c.Now, state: OK}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if q, err := c.Get(context.Background(), p, true); err != nil || q.State != OK {
				t.Errorf("%+v %v", q, err)
			}
		}()
	}
	wg.Wait()
	if p.calls.Load() != 1 {
		t.Fatalf("%d requests", p.calls.Load())
	}
	now = now.Add(30 * time.Second)
	c.Get(context.Background(), p, true)
	if p.calls.Load() != 1 {
		t.Fatal("cooldown ignored")
	}
	now = now.Add(31 * time.Second)
	c.Get(context.Background(), p, false)
	if p.calls.Load() != 1 {
		t.Fatal("TTL ignored")
	}
	c.Get(context.Background(), p, true)
	if p.calls.Load() != 2 {
		t.Fatal("refresh did not bypass TTL")
	}
	now = now.Add(6 * time.Minute)
	p.state = Unavailable
	p.retry = 15 * time.Minute
	q, err := c.Get(context.Background(), p, false)
	if err != nil || q.State != Stale || len(q.Windows) != 1 || !q.ObservedAt.Equal(testNow.Add(61*time.Second)) {
		t.Fatal(q, err)
	}
	now = now.Add(2 * time.Minute)
	c.Get(context.Background(), p, true)
	if p.calls.Load() != 3 {
		t.Fatal("refresh bypassed rate limit")
	}
	now = now.Add(20 * time.Minute)
	p.state = ExpiredAuth
	p.retry = 0
	q, err = c.Get(context.Background(), p, true)
	if err != nil || q.State != ExpiredAuth || len(q.Windows) != 0 {
		t.Fatal(q, err)
	}
}
func TestCacheStalePreservesNativePermission(t *testing.T) {
	now := testNow
	blocked := false
	c := Cache{Root: t.TempDir(), Now: func() time.Time { return now }}
	p := &fakeProvider{now: c.Now, state: OK, allowed: &blocked}
	if _, err := c.Get(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	p.state = Unavailable
	p.allowed = nil
	q, err := c.Get(context.Background(), p, false)
	if err != nil || q.State != Stale || q.IncludedUsageAllowed == nil || *q.IncludedUsageAllowed {
		t.Fatal(q, err)
	}
}

func TestCacheIdentityFailureDiscardsPriorObservation(t *testing.T) {
	now := testNow
	c := Cache{Root: t.TempDir(), Now: func() time.Time { return now }}
	p := &fakeProvider{now: c.Now, state: OK}
	if _, err := c.Get(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	p.state, p.preventStale = Unavailable, true
	q, err := c.Get(context.Background(), p, false)
	if err != nil || q.State != Unavailable || q.ObservedAt != nil || len(q.Windows) != 0 {
		t.Fatal(q, err)
	}
}

func TestCacheMalformedRebuild(t *testing.T) {
	c := Cache{Root: t.TempDir(), Now: func() time.Time { return testNow }}
	p := &fakeProvider{now: c.Now, state: OK}
	key := fingerprint(p.Account().Provider + ":" + p.Account().ID)
	path := filepath.Join(c.Root, key+".json")
	put(t, path, `{"version":2,"quota":`)
	if q, err := c.Get(context.Background(), p, false); err != nil || q.State != OK {
		t.Fatal(q, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record cachedQuota
	if json.Unmarshal(b, &record) != nil || record.Version != cacheFormatVersion {
		t.Fatal("bad cache")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe cache permissions")
	}
}
func TestCacheFailureCooldownWithoutProviderHint(t *testing.T) {
	for _, state := range []State{Unavailable, Unsupported, ExpiredAuth} {
		now := testNow
		c := Cache{Root: t.TempDir(), Now: func() time.Time { return now }}
		p := &fakeProvider{now: c.Now, state: state}
		if _, err := c.Get(context.Background(), p, false); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Minute)
		if _, err := c.Get(context.Background(), p, true); err != nil || p.calls.Load() != 1 {
			t.Fatal(state, err, p.calls.Load())
		}
	}
}

func TestCacheDataSymlinkIsNotRead(t *testing.T) {
	c := Cache{Root: t.TempDir(), Now: func() time.Time { return testNow }}
	p := &fakeProvider{now: c.Now, state: OK}
	target := filepath.Join(t.TempDir(), "outside.json")
	put(t, target, "private-data")
	key := fingerprint(p.Account().Provider + ":" + p.Account().ID)
	path := filepath.Join(c.Root, key+".json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readSmall(path); err == nil {
		t.Fatal("followed cache-file symlink")
	}
	if q, err := c.Get(context.Background(), p, false); err != nil || q.State != OK {
		t.Fatal(q, err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "private-data" {
		t.Fatal(string(b), err)
	}
}

func TestCacheCanceledLockAndSymlink(t *testing.T) {
	c := Cache{Root: t.TempDir()}
	p := &fakeProvider{now: func() time.Time { return testNow }, state: OK}
	key := fingerprint(p.Account().Provider + ":" + p.Account().ID)
	target := filepath.Join(t.TempDir(), "target")
	put(t, target, "unchanged")
	if err := os.Symlink(target, filepath.Join(c.Root, key+".json.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(context.Background(), p, false); err == nil {
		t.Fatal("symlink lock accepted")
	}
	if p.calls.Load() != 0 {
		t.Fatal("provider called without safe cache lock")
	}
}
