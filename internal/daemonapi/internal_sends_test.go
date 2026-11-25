package daemonapi

import (
	"context"
	"github.com/regb/workitem/internal/model"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// The daemon sends on its own, for a schedule's run or a usage follow-up,
// with no request to take an identity from. It takes control as a valid
// holder, and gives it back; when a client holds control, it acts for it.
func TestTheDaemonTakesControlForItsOwnSendsAndGivesItBack(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.repo(t), Title: "scheduled"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	if _, err := f.clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	// A context with no caller, as the scheduler's.
	own, done := f.api.actOnItself(context.Background(), id, scheduleClient, "schedule", model.Origin{Actor: model.OperatorUser, Cause: "schedule:s1"})
	lease, err := f.api.controlLeaseStarting(own, id, "", true, false)
	if err != nil {
		t.Fatalf("control for the daemon's own send: %v", err)
	}
	if lease.ClientID != scheduleClient || lease.Holder != "cli" {
		t.Fatalf("lease = %+v", lease)
	}
	done(nil)
	if current, err := f.api.Local.Lease(ctx, id); err != nil || current != nil {
		t.Fatalf("control kept after the send: %+v %v", current, err)
	}
	// With a client in control, the daemon acts for it and leaves it be.
	acquire := connect.NewRequest(&v1.AcquireControlRequest{ItemId: id, ClientId: "tab"})
	acquire.Header().Set(ClientHeader, "tab")
	if _, err := f.clients.Control.AcquireControl(ctx, acquire); err != nil {
		t.Fatal(err)
	}
	own, done = f.api.actOnItself(context.Background(), id, followUpClient, "follow-up", model.Origin{Actor: model.OperatorUser, Cause: "follow-up:f1"})
	if lease, err := f.api.controlLeaseStarting(own, id, "", true, false); err != nil || lease.ClientID != "tab" {
		t.Fatalf("follow-up displaced the controller: %+v %v", lease, err)
	}
	done(nil)
	if current, err := f.api.Local.Lease(ctx, id); err != nil || current == nil || current.ClientID != "tab" {
		t.Fatalf("controller's lease after the follow-up: %+v %v", current, err)
	}
}
