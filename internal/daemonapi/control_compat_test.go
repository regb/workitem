package daemonapi

import (
	"connectrpc.com/connect"
	"context"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"strings"
	"testing"
)

func TestExplicitControlDoesNotStartOfflineAgent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repository := f.repo(t)
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repository, Title: "offline control"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	if _, err := f.clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	if _, err := f.clients.Workspaces.EnsureWorkspace(ctx, connect.NewRequest(&v1.EnsureWorkspaceRequest{ItemId: id, SkipSetup: true})); err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&v1.SendMessageRequest{ItemId: id, Text: "must not start", RequireOnline: true})
	req.Header().Set(ControlActorHeader, "test")
	_, err = f.clients.Agents.SendMessage(ctx, req)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal(err)
	}
	item, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
	if err != nil || item.Msg.Item.Conversation != nil {
		t.Fatalf("created conversation: %v %v", item, err)
	}
	req.Header().Set(ControlActorHeader, strings.Repeat("x", 129))
	_, err = f.clients.Agents.SendMessage(ctx, req)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid actor: %v", err)
	}
}
