package daemonapi_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
)

func TestExplicitControlDoesNotStartOfflineAgent(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	repository := f.Repository(t, "test")
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repository, Title: "offline control"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Clients.Workspaces.EnsureWorkspace(ctx, connect.NewRequest(&v1.EnsureWorkspaceRequest{ItemId: id, SkipSetup: true})); err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&v1.SendMessageRequest{ItemId: id, Text: "must not start", RequireOnline: true})
	_, err = f.Clients.Agents.SendMessage(ctx, req)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal(err)
	}
	item, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
	if err != nil || item.Msg.Item.Conversation != nil {
		t.Fatalf("created conversation: %v %v", item, err)
	}
}
