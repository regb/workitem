package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/regb/workitem/internal/lock"
)

// Cache serializes observations across CLI processes. Files contain normalized
// observations only, never credential documents, HTTP bodies, or raw errors.
type Cache struct {
	Root string
	Now  func() time.Time
}

const cacheFormatVersion = 5

type cachedQuota struct {
	Version      int       `json:"version"`
	Quota        Quota     `json:"quota"`
	RefreshAfter time.Time `json:"refresh_after"`
}

func (c Cache) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
func (c Cache) Get(ctx context.Context, p Provider, refresh bool) (Quota, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if resolver, ok := p.(AccountResolver); ok {
		p = resolver.ResolveAccount(ctx)
	}
	started := c.now()
	key := fingerprint(p.Account().Provider + ":" + p.Account().ID)
	path := filepath.Join(c.Root, key+".json")
	l, err := lock.Acquire(ctx, path+".lock")
	if err != nil {
		return Quota{}, fmt.Errorf("usage cache is unavailable")
	}
	defer l.Release()
	var previous cachedQuota
	if b, err := readSmall(path); err == nil {
		if json.Unmarshal(b, &previous) != nil {
			previous = cachedQuota{}
		}
	}
	now := c.now()
	valid := previous.Version == cacheFormatVersion && previous.Quota.Account == p.Account() && !previous.Quota.CheckedAt.After(now)
	if valid && (now.Before(previous.RefreshAfter) || (!refresh && previous.Quota.NextRefreshAt != nil && now.Before(*previous.Quota.NextRefreshAt)) || !previous.Quota.CheckedAt.Before(started)) {
		q := previous.Quota
		if validator, ok := p.(CachedQuotaValidator); ok {
			q = validator.ValidateCachedQuota(ctx, q)
		}
		return AgeObservation(q, now), nil
	}
	q := p.Quota(ctx)
	// Even explicit refresh is limited to once per minute. Ordinary reads use a
	// five-minute TTL. Rate-limit Retry-After overrides both.
	refreshAfter := now.Add(time.Minute)
	next := now.Add(5 * time.Minute)
	if q.State != OK {
		refreshAfter = next
	}
	if q.NextRefreshAt != nil {
		if q.NextRefreshAt.After(next) {
			next = *q.NextRefreshAt
		}
		refreshAfter = next
	}
	q.NextRefreshAt = &next
	if valid && !q.PreventStaleFallback && q.State == Unavailable && previous.Quota.ObservedAt != nil {
		q.State = Stale
		q.ObservedAt = previous.Quota.ObservedAt
		q.Windows = previous.Quota.Windows
		q.Credits = previous.Quota.Credits
		q.IncludedUsageAllowed = previous.Quota.IncludedUsageAllowed
	}
	data, err := json.Marshal(cachedQuota{cacheFormatVersion, q, refreshAfter})
	if err != nil {
		return Quota{}, fmt.Errorf("encode usage cache")
	}
	// Same-directory temporary file and rename keep interrupted writes rebuildable.
	f, err := os.CreateTemp(c.Root, ".usage-*")
	if err != nil {
		return Quota{}, fmt.Errorf("write usage cache")
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return Quota{}, fmt.Errorf("write usage cache")
	}
	if err = f.Close(); err != nil {
		return Quota{}, fmt.Errorf("write usage cache")
	}
	if err = os.Rename(temp, path); err != nil {
		return Quota{}, fmt.Errorf("write usage cache")
	}
	return AgeObservation(q, now), nil
}
