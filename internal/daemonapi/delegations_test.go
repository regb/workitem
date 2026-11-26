package daemonapi_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

// An item's agent delegates what it creates: the item is its agent's to
// follow up, out of the user's list, until the user takes it or the owning
// item closes and gives it back.
func TestAgentsDelegateTheItemsTheyCreate(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	repo := f.Repository(t, "test")
	parent, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "parent"}))
	if err != nil {
		t.Fatal(err)
	}
	parentID := parent.Msg.Item.Id
	if parent.Msg.Item.Owner != model.OperatorUser {
		t.Fatalf("the operator's item is owned by %q", parent.Msg.Item.Owner)
	}
	setState := func(state v1.ItemState) {
		t.Helper()
		if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: parentID, State: state})); err != nil {
			t.Fatal(err)
		}
	}
	setState(v1.ItemState_ITEM_STATE_ACTIVE)
	agent, _ := f.AgentClients(t, parentID)
	delegated := []string{}
	for i := 0; i < 3; i++ {
		child, err := agent.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: fmt.Sprintf("child %d", i)}))
		if err != nil {
			t.Fatal(err)
		}
		if child.Msg.Item.Owner != model.ItemPrincipal(parentID) {
			t.Fatalf("delegated item owned by %q", child.Msg.Item.Owner)
		}
		delegated = append(delegated, child.Msg.Item.Id)
	}
	list := func(owner string) map[string]bool {
		t.Helper()
		value, err := f.Clients.Items.ListItems(ctx, connect.NewRequest(&v1.ListItemsRequest{Owner: owner}))
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
	// Whatever its owner's state, a delegation's attention goes to its owner.
	setState(v1.ItemState_ITEM_STATE_BACKLOG)
	if mine := list(""); mine[delegated[0]] {
		t.Fatal("a shelved owner's delegation is in the user's list")
	}
	setState(v1.ItemState_ITEM_STATE_ACTIVE)
	// Rights reach down the owner chain: what a delegation delegates is its
	// owner's too.
	childAgent, _ := f.AgentClients(t, delegated[1])
	grandchild, err := childAgent.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "grandchild"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Clients.Items.RevokeAccess(ctx, connect.NewRequest(&v1.RevokeAccessRequest{EntryId: coordinator.DefaultAccessID})); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Items.UpdateItem(ctx, connect.NewRequest(&v1.UpdateItemRequest{ItemId: grandchild.Msg.Item.Id, AddLabels: []string{"x"}})); err != nil {
		t.Fatalf("managing a delegation's delegation: %v", err)
	}
	if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: grandchild.Msg.Item.Id, State: v1.ItemState_ITEM_STATE_CLOSED, Resolution: v1.Resolution_RESOLUTION_ABANDONED})); err != nil {
		t.Fatal(err)
	}
	// Only a person changes an owner.
	setOwner := func(clients interface {
		UpdateItem(context.Context, *connect.Request[v1.UpdateItemRequest]) (*connect.Response[v1.UpdateItemResponse], error)
	}, id, owner string) (*v1.Item, error) {
		value, err := clients.UpdateItem(ctx, connect.NewRequest(&v1.UpdateItemRequest{ItemId: id, Owner: owner}))
		if err != nil {
			return nil, err
		}
		return value.Msg.Item, nil
	}
	if _, err := setOwner(agent.Items, delegated[0], model.OperatorUser); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an agent changed an owner: %v", err)
	}
	taken, err := setOwner(f.Clients.Items, delegated[0], model.OperatorUser)
	if err != nil || taken.Owner != model.OperatorUser {
		t.Fatalf("take: %v %v", taken, err)
	}
	if !list("")[delegated[0]] {
		t.Fatal("a taken item is not in the user's list")
	}
	// A person hands an item to another item; no item owns itself, or an
	// item that owns it.
	if handed, err := setOwner(f.Clients.Items, delegated[0], model.ItemPrincipal(delegated[1])); err != nil || handed.Owner != model.ItemPrincipal(delegated[1]) {
		t.Fatalf("hand over: %v %v", handed, err)
	}
	if _, err := setOwner(f.Clients.Items, delegated[1], model.ItemPrincipal(delegated[1])); err == nil {
		t.Fatal("an item owns itself")
	}
	if _, err := setOwner(f.Clients.Items, parentID, model.ItemPrincipal(delegated[0])); err == nil {
		t.Fatal("an item owns its own owner")
	}
	if _, err := setOwner(f.Clients.Items, delegated[0], model.OperatorUser); err != nil {
		t.Fatal(err)
	}
	// Closing the owning item gives the rest back, unread.
	if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: parentID, State: v1.ItemState_ITEM_STATE_CLOSED, Resolution: v1.Resolution_RESOLUTION_COMPLETED})); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		back := 0
		for _, id := range delegated {
			if got, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id})); err == nil && got.Msg.Item.Owner == model.OperatorUser {
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
	got, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: delegated[1]}))
	if err != nil || got.Msg.Item.Owner != model.OperatorUser || !got.Msg.Observation.Unread {
		t.Fatalf("returned item: %v %v", got, err)
	}
	// A closed item owns nothing new.
	if _, err := agent.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "after close"})); err == nil {
		t.Fatal("a closed item delegated")
	}
}
