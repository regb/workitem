package daemonapi

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	p "github.com/regb/workitem/agentdriver/protocol"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func TestNativeSettingsAreAdoptedByTheDaemonAndFenceOldOwners(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repository := f.repo(t)
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repository, Title: "native settings"}))
	if err != nil {
		t.Fatal(err)
	}
	item := created.Msg.Item.Id
	if _, err := f.clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: item, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	workspace, err := f.clients.Workspaces.EnsureWorkspace(ctx, connect.NewRequest(&v1.EnsureWorkspaceRequest{ItemId: item, SkipSetup: true}))
	if err != nil {
		t.Fatal(err)
	}
	claim := workspace.Msg.Checkout.Claim
	runtime := model.AgentRuntime{ID: id(), WorkItemID: item, ConversationID: id(), Mode: "headless", State: model.AgentRuntimeRunning, StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), WorkspaceClaimID: claim.Id, WorkspaceClaimGeneration: uint64(claim.Generation)}
	if err := f.api.App.Store.SaveAgentRuntime(ctx, item, runtime); err != nil {
		t.Fatal(err)
	}
	wait := func(value string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			item, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: item}))
			if err == nil && item.Msg.Item.AgentSettings["model"] == value {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("native setting not adopted: %v %v", item, err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	for _, value := range []string{"test/native", "test/changed"} {
		_, err := f.core.LocalClient().IngestRuntimeEvent(ctx, coordinator.RuntimeSemanticEvent{ID: id(), RuntimeID: runtime.ID, WorkItemID: item, Type: p.EventSettingsChanged, Timestamp: time.Now().UTC(), Settings: map[string]string{"model": value}})
		if err != nil {
			t.Fatal(err)
		}
		wait(value)
	}
	oldID := runtime.ID
	runtime.ID = id()
	runtime.StartedAt = time.Now().UTC()
	runtime.UpdatedAt = runtime.StartedAt
	if err := f.api.App.Store.SaveAgentRuntime(ctx, item, runtime); err != nil {
		t.Fatal(err)
	}
	if err := f.api.App.AdoptRuntimeSettings(ctx, item, oldID, map[string]string{"model": "test/stale"}, true); err == nil {
		t.Fatal("old runtime changed saved settings")
	}
	wait("test/changed")
}
