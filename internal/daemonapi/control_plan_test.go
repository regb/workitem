package daemonapi

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/coordinator"
)

func TestCallerIdentityComesFromListenerNotMetadata(t *testing.T) {
	f := newFixture(t)
	for _, test := range []struct {
		path   string
		access v1.CallerAccess
		kind   v1.CallerKind
	}{{f.root + "/api.sock", v1.CallerAccess_CALLER_ACCESS_OPERATOR, v1.CallerKind_CALLER_KIND_LOCAL_OPERATOR}, {f.root + "/agent.sock", v1.CallerAccess_CALLER_ACCESS_AGENT, v1.CallerKind_CALLER_KIND_AGENT}, {coordinator.WebSocketPath(f.root + "/api.sock"), v1.CallerAccess_CALLER_ACCESS_OPERATOR, v1.CallerKind_CALLER_KIND_WEB_ADAPTER}} {
		clients, err := apitransport.NewClients(test.path)
		if err != nil {
			t.Fatal(err)
		}
		req := connect.NewRequest(&v1.GetInfoRequest{})
		req.Header().Set(HolderHeader, "agent")
		req.Header().Set("Wi-Endpoint-Access", "operator")
		value, err := clients.System.GetInfo(context.Background(), req)
		clients.Close()
		if err != nil {
			t.Fatal(err)
		}
		if value.Msg.Caller.Access != test.access || value.Msg.Caller.Kind != test.kind || value.Header().Get("Wi-Endpoint-Access") != "" {
			t.Fatalf("caller: %v %v", value.Msg, value.Header())
		}
	}
}

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
