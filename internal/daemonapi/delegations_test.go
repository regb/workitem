package daemonapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

// An item's agent delegates what it creates: the item is its agent's to
// follow up, out of the user's list, until the user takes it or the owning
// item closes and gives it back.
func TestAgentsDelegateTheItemsTheyCreate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo := f.repo(t)
	parent, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "parent"}))
	if err != nil {
		t.Fatal(err)
	}
	parentID := parent.Msg.Item.Id
	if parent.Msg.Item.Owner != model.OperatorUser {
		t.Fatalf("the operator's item is owned by %q", parent.Msg.Item.Owner)
	}
	agent, _ := f.agentClients(t, parentID)
	delegated := []string{}
	for i := 0; i < coordinator.MaxOpenDelegations; i++ {
		child, err := agent.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: fmt.Sprintf("child %d", i)}))
		if err != nil {
			t.Fatal(err)
		}
		if child.Msg.Item.Owner != model.ItemPrincipal(parentID) {
			t.Fatalf("delegated item owned by %q", child.Msg.Item.Owner)
		}
		delegated = append(delegated, child.Msg.Item.Id)
	}
	if _, err := agent.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "one too many"})); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("delegation cap: %v", err)
	}
	list := func(owner string) map[string]bool {
		t.Helper()
		value, err := f.clients.Items.ListItems(ctx, connect.NewRequest(&v1.ListItemsRequest{Owner: owner}))
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, row := range value.Msg.Items {
			ids[row.Id] = true
		}
		return ids
	}
	if mine := list(""); !mine[parentID] || mine[delegated[0]] {
		t.Fatalf("the user's list: %v", mine)
	}
	if theirs := list(model.ItemPrincipal(parentID)); len(theirs) != len(delegated) {
		t.Fatalf("the parent's delegations: %v", theirs)
	}
	// Only a person takes an item over.
	if _, err := agent.Items.TakeOwnership(ctx, connect.NewRequest(&v1.TakeOwnershipRequest{ItemId: delegated[0]})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an agent took an item: %v", err)
	}
	taken, err := f.clients.Items.TakeOwnership(ctx, connect.NewRequest(&v1.TakeOwnershipRequest{ItemId: delegated[0]}))
	if err != nil || taken.Msg.Item.Owner != model.OperatorUser {
		t.Fatalf("take: %v %v", taken, err)
	}
	if !list("")[delegated[0]] {
		t.Fatal("a taken item is not in the user's list")
	}
	// Closing the owning item gives the rest back, unread.
	if _, err := f.clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: parentID, State: v1.ItemState_ITEM_STATE_CLOSED, Resolution: v1.Resolution_RESOLUTION_COMPLETED})); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mine := list("")
		back := 0
		for _, id := range delegated {
			if mine[id] {
				back++
			}
		}
		if back == len(delegated) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d delegations returned", back, len(delegated))
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: delegated[1]}))
	if err != nil || got.Msg.Item.Owner != model.OperatorUser || !got.Msg.Observation.Unread {
		t.Fatalf("returned item: %v %v", got, err)
	}
	// A closed item owns nothing new.
	if _, err := agent.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "after close"})); err == nil {
		t.Fatal("a closed item delegated")
	}
}
