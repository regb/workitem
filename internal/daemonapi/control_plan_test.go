package daemonapi

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestConfirmedControlBindsPlanAndIgnoresLeaseRenewal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo := f.repo(t)
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "control plans"}))
	if err != nil {
		t.Fatal(err)
	}
	item := created.Msg.Item.Id
	if _, err := f.clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: item, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	plan := func(client string) *v1.ControlPlan {
		t.Helper()
		req := connect.NewRequest(&v1.GetControlRequest{ItemId: item, ClientId: client})
		req.Header().Set(ClientHeader, client)
		value, err := f.clients.Control.GetControl(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if value.Msg.Control.Plan.PlanToken == "" {
			t.Fatal("missing plan token")
		}
		return value.Msg.Control.Plan
	}
	acquire := func(client, token string, confirm bool) (*connect.Response[v1.AcquireControlResponse], error) {
		req := connect.NewRequest(&v1.AcquireControlRequest{ItemId: item, ClientId: client, Confirm: confirm, PlanToken: token})
		req.Header().Set(ClientHeader, client)
		return f.clients.Control.AcquireControl(ctx, req)
	}
	alpha, err := acquire("alpha", "", false)
	if err != nil {
		t.Fatal(err)
	}
	beta := plan("beta")
	if !beta.NeedsConfirmation {
		t.Fatal(beta)
	}
	if _, err := acquire("beta", "", true); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("missing token: %v", err)
	}
	renew := connect.NewRequest(&v1.RenewControlRequest{LeaseId: alpha.Msg.Control.Lease.Id})
	renew.Header().Set(ClientHeader, "alpha")
	if _, err := f.clients.Control.RenewControl(ctx, renew); err != nil {
		t.Fatal(err)
	}
	taken, err := acquire("beta", beta.PlanToken, true)
	if err != nil {
		t.Fatalf("renewal invalidated unchanged plan: %v", err)
	}
	if _, err := acquire("alpha", beta.PlanToken, true); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("token reused by another caller: %v", err)
	}
	stale := plan("alpha")
	release := connect.NewRequest(&v1.ReleaseControlRequest{LeaseId: taken.Msg.Control.Lease.Id})
	release.Header().Set(ClientHeader, "beta")
	if _, err := f.clients.Control.ReleaseControl(ctx, release); err != nil {
		t.Fatal(err)
	}
	if _, err := acquire("alpha", stale.PlanToken, true); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("changed controller accepted: %v", err)
	}
	if _, err := acquire("alpha", plan("alpha").PlanToken, true); err != nil {
		t.Fatal(err)
	}
}
