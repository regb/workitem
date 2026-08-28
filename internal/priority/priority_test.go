package priority_test

import (
	"testing"
	"time"

	"github.com/regb/workitem/internal/priority"
)

func TestRankHotThenDeferredRoundRobin(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	tm := func(minutes int) *time.Time { v := base.Add(time.Duration(minutes) * time.Minute); return &v }
	candidates := []priority.Candidate{
		{ID: "A", BaseOrder: 0, LastRequestedAt: tm(1), LastDeferredAt: tm(10)},
		{ID: "E", BaseOrder: 1, LastRequestedAt: tm(3)},
		{ID: "F", BaseOrder: 2, LastRequestedAt: tm(2)},
		{ID: "B", BaseOrder: 3, LastRequestedAt: tm(8)},
		{ID: "D", BaseOrder: 4, LastDeferredAt: tm(12)},
	}
	ranked, err := priority.Rank(priority.DefaultStrategy, candidates)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"B", "E", "F", "A", "D"}
	for i := range want {
		if ranked[i].ID != want[i] {
			t.Fatalf("ranked = %+v, want %v", ranked, want)
		}
	}
}

func TestRequestAfterDeferRanksWithNonDeferredCandidates(t *testing.T) {
	deferred := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	requested := deferred.Add(time.Minute)
	otherDefer := deferred.Add(-time.Minute)
	ranked, err := priority.Rank(priority.DefaultStrategy, []priority.Candidate{
		{ID: "B", LastDeferredAt: &otherDefer},
		{ID: "A", LastDeferredAt: &deferred, LastRequestedAt: &requested},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].ID != "A" {
		t.Fatalf("newer request should supersede older defer: %+v", ranked)
	}
}

type reverseIDStrategy struct{}

func (reverseIDStrategy) Name() string                      { return "reverse-id-test" }
func (reverseIDStrategy) Less(a, b priority.Candidate) bool { return a.ID > b.ID }

func TestRegisteredStrategyCanBeSelected(t *testing.T) {
	priority.Register(reverseIDStrategy{})
	ranked, err := priority.Rank("reverse-id-test", []priority.Candidate{{ID: "A"}, {ID: "B"}})
	if err != nil {
		t.Fatal(err)
	}
	if ranked[0].ID != "B" {
		t.Fatalf("ranked = %+v", ranked)
	}
}

func TestUnknownStrategyFails(t *testing.T) {
	if _, err := priority.Rank("missing", nil); err == nil {
		t.Fatal("expected unknown strategy to fail")
	}
}
