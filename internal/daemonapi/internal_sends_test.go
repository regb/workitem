package daemonapi_test

import (
	"context"
	"testing"

	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/daemonapi"
	"github.com/regb/workitem/internal/model"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// The daemon sends on its own, for a schedule's run or a usage follow-up,
// with no request to take an identity from. It takes control as a valid
// holder, and gives it back; when a client holds control, it acts for it.
func TestTheDaemonTakesControlForItsOwnSendsAndGivesItBack(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.Repository(t, "test"), Title: "scheduled"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	// A context with no caller, as the scheduler's.
	own, done := f.API().ActOnItself(context.Background(), id, daemonapi.ScheduleClient, "schedule", model.Origin{Actor: model.OperatorUser, Cause: "schedule:s1"})
	lease, err := f.API().ControlLeaseStarting(own, id, "", true, false)
	if err != nil {
		t.Fatalf("control for the daemon's own send: %v", err)
	}
	if lease.ClientID != daemonapi.ScheduleClient || lease.Holder != "cli" {
		t.Fatalf("lease = %+v", lease)
	}
	done(nil)
	if current, err := f.API().Local.Lease(ctx, id); err != nil || current != nil {
		t.Fatalf("control kept after the send: %+v %v", current, err)
	}
	// With a client in control, the daemon acts for it and leaves it be.
	acquire := connect.NewRequest(&v1.AcquireControlRequest{ItemId: id, ClientId: "tab"})
	acquire.Header().Set(daemonapi.ClientHeader, "tab")
	if _, err := f.Clients.Control.AcquireControl(ctx, acquire); err != nil {
		t.Fatal(err)
	}
	own, done = f.API().ActOnItself(context.Background(), id, daemonapi.FollowUpClient, "follow-up", model.Origin{Actor: model.OperatorUser, Cause: "follow-up:f1"})
	if lease, err := f.API().ControlLeaseStarting(own, id, "", true, false); err != nil || lease.ClientID != "user:operator/tab" {
		t.Fatalf("follow-up displaced the controller: %+v %v", lease, err)
	}
	done(nil)
	if current, err := f.API().Local.Lease(ctx, id); err != nil || current == nil || current.ClientID != "user:operator/tab" {
		t.Fatalf("controller's lease after the follow-up: %+v %v", current, err)
	}
}
