package daemonapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apiview"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUsageProviderFollowsTheDriverAndModel(t *testing.T) {
	for _, c := range []struct{ driver, model, want string }{
		{"claude", "claude-opus-5-5", "claude"},
		{"pi", "openai-codex/gpt-6.1", "codex"},
		{"pi", "anthropic/claude-sonnet-5-5", "claude"},
		{"pi", "opencode-go/glm-5", "opencode-go"},
		{"pi", "unqualified", ""},
	} {
		if got := usageProvider(c.driver, c.model); got != c.want {
			t.Errorf("usageProvider(%q, %q) = %q, want %q", c.driver, c.model, got, c.want)
		}
	}
}

func TestQuotaAvailabilityWaitsForEveryExhaustedWindow(t *testing.T) {
	soon, later := time.Now().Add(time.Hour), time.Now().Add(24*time.Hour)
	window := func(remaining float64, reset time.Time) *v1.UsageWindow {
		return &v1.UsageWindow{RemainingPercent: proto.Float64(remaining), ResetsAt: timestamppb.New(reset)}
	}
	available, reset := quotaAvailability(&v1.UsageQuota{Windows: []*v1.UsageWindow{window(0, soon), window(0, later), window(40, soon)}})
	if available || reset == nil || !reset.Equal(later) {
		t.Fatalf("exhausted: %v %v", available, reset)
	}
	if available, reset := quotaAvailability(&v1.UsageQuota{Windows: []*v1.UsageWindow{window(10, soon), {}}}); !available || reset != nil {
		t.Fatalf("available: %v %v", available, reset)
	}
	if available, _ := quotaAvailability(&v1.UsageQuota{IncludedUsageAllowed: proto.Bool(false)}); available {
		t.Fatal("a refused subscription is available")
	}
}

func TestUsageBlockerClearsAfterACheckPastItsReset(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo := f.repo(t)
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "Limited"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	reset := time.Now().Add(-time.Minute)
	added, err := f.clients.Items.AddBlocker(ctx, connect.NewRequest(&v1.AddBlockerRequest{ItemId: id, Kind: v1.BlockerKind_BLOCKER_KIND_USAGE, Provider: "claude", Until: timestamppb.New(reset), FollowUp: "Continue where you left off."}))
	if err != nil {
		t.Fatal(err)
	}
	blocker := apiview.Blockers([]*v1.Blocker{added.Msg.Blocker})[0]
	var mu sync.Mutex
	remaining := 0.0
	checks := 0
	quota := func() *v1.GetUsageResponse {
		return &v1.GetUsageResponse{Enabled: true, Quotas: []*v1.UsageQuota{{Provider: "claude", Windows: []*v1.UsageWindow{{RemainingPercent: proto.Float64(remaining), ResetsAt: timestamppb.New(time.Now().Add(time.Hour))}}}}}
	}
	f.api.usage.run = func(context.Context, bool) (*v1.GetUsageResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		checks++
		return quota(), nil
	}
	// The last check predates the reset: the daemon checks again.
	f.api.usage.mu.Lock()
	f.api.usage.result, f.api.usage.checkedAt = quota(), reset.Add(-time.Minute)
	f.api.usage.mu.Unlock()
	count := func() int { mu.Lock(); defer mu.Unlock(); return checks }
	clear := func() []*v1.Blocker {
		t.Helper()
		value, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
		if err != nil {
			t.Fatal(err)
		}
		m := apiview.Manifest(value.Msg.Item)
		f.api.clearUsageBlocker(ctx, m, blocker, time.Now())
		for deadline := time.Now().Add(5 * time.Second); f.api.usageSnapshot().Refreshing && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
		value, err = f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Item.Blockers
	}
	if blockers := clear(); len(blockers) != 1 || count() != 1 {
		t.Fatalf("before a fresh check: %v, %d checks", blockers, count())
	}
	// A fresh check still shows the limit: the item keeps waiting.
	if blockers := clear(); len(blockers) != 1 || count() != 1 {
		t.Fatalf("still limited: %v, %d checks", blockers, count())
	}
	mu.Lock()
	remaining = 30
	mu.Unlock()
	f.api.usage.mu.Lock()
	f.api.usage.result, f.api.usage.checkedAt = quota(), time.Now()
	f.api.usage.mu.Unlock()
	if blockers := clear(); len(blockers) != 0 {
		t.Fatalf("quota is back: %v", blockers)
	}
	value, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
	if err != nil {
		t.Fatal(err)
	}
	if followUps := value.Msg.Item.FollowUps; len(followUps) != 1 || followUps[0].Message != "Continue where you left off." {
		t.Fatalf("follow-ups: %v", followUps)
	}
}
