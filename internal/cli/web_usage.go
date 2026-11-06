package cli

import (
	"context"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/usage"
	webaccess "github.com/regb/workitem/internal/web"
)

// Usage reads the daemon's last usage result at once; refresh asks the
// daemon to check again in the background, and the page polls while it
// runs. Only the first read after the daemon starts waits for a check.
func (b webBackend) Usage(ctx context.Context, refresh bool) (webaccess.Usage, error) {
	value, err := b.cfg.API.System.GetUsage(ctx, webRequest(ctx, &v1.GetUsageRequest{Refresh: refresh}))
	if err != nil {
		return webaccess.Usage{Results: []usage.QuotaResult{}}, err
	}
	m := value.Msg
	out := webaccess.Usage{Enabled: m.Enabled, Refreshing: m.Refreshing, Error: m.Error, Results: []usage.QuotaResult{}}
	if m.CheckedAt != nil {
		at := m.CheckedAt.AsTime()
		out.CheckedAt = &at
	}
	for _, q := range m.Quotas {
		out.Results = append(out.Results, quotaResult(q))
	}
	return out, nil
}

// quotaResult converts the API's quota to the page's shape, which is the
// usage plugin's own JSON.
func quotaResult(q *v1.UsageQuota) usage.QuotaResult {
	out := usage.QuotaResult{Provider: q.Provider, Account: q.Account, IncludedUsageAllowed: q.IncludedUsageAllowed, Stale: q.Stale, Windows: []usage.Window{}}
	if q.ObservedAt != nil {
		at := q.ObservedAt.AsTime()
		out.ObservedAt = &at
	}
	for _, w := range q.Windows {
		window := usage.Window{ID: w.Id, RemainingPercent: w.RemainingPercent, DurationSeconds: w.DurationSeconds}
		if w.ScopeModel != "" || w.ScopeSurface != "" {
			window.Scope = &usage.WindowScope{Model: w.ScopeModel, Surface: w.ScopeSurface}
		}
		if w.ResetsAt != nil {
			at := w.ResetsAt.AsTime()
			window.ResetsAt = &at
		}
		out.Windows = append(out.Windows, window)
	}
	if q.Credits != nil {
		out.Credits = &usage.Credits{Remaining: q.Credits.Remaining, Unlimited: q.Credits.Unlimited, Unit: q.Credits.Unit}
	}
	if q.ErrorCode != "" || q.ErrorMessage != "" {
		out.Error = &usage.QuotaError{Code: usage.State(q.ErrorCode), Message: q.ErrorMessage}
	}
	return out
}
