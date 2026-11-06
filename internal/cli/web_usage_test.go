package cli

import (
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUsageQuotaKeepsThePluginShape(t *testing.T) {
	percent, seconds, allowed := 42.5, int64(18000), false
	at := time.Date(2026, 11, 19, 15, 0, 0, 0, time.UTC)
	got := quotaResult(&v1.UsageQuota{Provider: "codex", Account: "slot", ObservedAt: timestamppb.New(at), IncludedUsageAllowed: &allowed, Stale: true,
		Windows:   []*v1.UsageWindow{{Id: "primary", ScopeModel: "gpt", RemainingPercent: &percent, ResetsAt: timestamppb.New(at.Add(time.Hour)), DurationSeconds: &seconds}, {Id: "unknown"}},
		Credits:   &v1.UsageCredits{Unit: "credits"},
		ErrorCode: "stale", ErrorMessage: "Retry later"})
	if got.Provider != "codex" || got.Account != "slot" || !got.ObservedAt.Equal(at) || *got.IncludedUsageAllowed || !got.Stale || got.Credits.Unit != "credits" || got.Error.Message != "Retry later" {
		t.Fatalf("quota: %+v", got)
	}
	if w := got.Windows[0]; *w.RemainingPercent != percent || *w.DurationSeconds != seconds || w.Scope.Model != "gpt" || !w.ResetsAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("window: %+v", w)
	}
	// Unknown stays unknown: no scope, no percent, never zero.
	if w := got.Windows[1]; w.Scope != nil || w.RemainingPercent != nil || w.ResetsAt != nil {
		t.Fatalf("unknown window: %+v", w)
	}
}
