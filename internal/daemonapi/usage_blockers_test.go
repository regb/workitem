package daemonapi_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/apiview"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUsageBlockerClearsAfterACheckPastItsReset(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	repo := f.Repository(t, "test")
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "Limited"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	reset := time.Now().Add(-time.Minute)
	added, err := f.Clients.Items.AddBlocker(ctx, connect.NewRequest(&v1.AddBlockerRequest{ItemId: id, Kind: v1.BlockerKind_BLOCKER_KIND_USAGE, Provider: "claude", Until: timestamppb.New(reset), FollowUp: "Continue where you left off."}))
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
	f.API().SetUsageCheck(func(context.Context, bool) (*v1.GetUsageResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		checks++
		return quota(), nil
	})
	// The last check predates the reset: the daemon checks again.
	f.API().SetUsageResult(quota(), reset.Add(-time.Minute))
	count := func() int { mu.Lock(); defer mu.Unlock(); return checks }
	clear := func() []*v1.Blocker {
		t.Helper()
		value, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
		if err != nil {
			t.Fatal(err)
		}
		m, err := f.App().Store.LoadManifest(value.Msg.Item.Id)
		if err != nil {
			t.Fatal(err)
		}
		f.API().ClearUsageBlocker(ctx, m, blocker, time.Now())
		for deadline := time.Now().Add(5 * time.Second); f.API().UsageSnapshot().Refreshing && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
		value, err = f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
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
	f.API().SetUsageResult(quota(), time.Now())
	if blockers := clear(); len(blockers) != 0 {
		t.Fatalf("quota is back: %v", blockers)
	}
	value, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
	if err != nil {
		t.Fatal(err)
	}
	if followUps := value.Msg.Item.FollowUps; len(followUps) != 1 || followUps[0].Message != "Continue where you left off." {
		t.Fatalf("follow-ups: %v", followUps)
	}
}
