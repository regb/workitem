package daemonapi_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/daemonapi"
	"github.com/regb/workitem/internal/model"
)

// controlAs is request sent by client, of holder kind if one is given.
func controlAs[T any](request *T, client, holder string) *connect.Request[T] {
	value := connect.NewRequest(request)
	if client != "" {
		value.Header().Set(daemonapi.ClientHeader, client)
	}
	if holder != "" {
		value.Header().Set(daemonapi.HolderHeader, holder)
	}
	return value
}

// controlStatus is the control status a refusal carries.
func controlStatus(t testing.TB, err error) *v1.ControlStatus {
	t.Helper()
	var refusal *connect.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("not a refusal: %v", err)
	}
	for _, detail := range refusal.Details() {
		if value, err := detail.Value(); err == nil {
			if control, ok := value.(*v1.ControlStatus); ok {
				return control
			}
		}
	}
	t.Fatalf("refusal without control status: %v", err)
	return nil
}

// Control of an item's agent: plans and their tokens, acquire, renew,
// release, refusals, takeover, and whose a lease is. None of it needs a
// running agent.
func TestControlLeasesAndPlans(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repository := f.Repository(t, "test")
	active := func(title string) string {
		t.Helper()
		id := newItem(t, f, repository, title).Id
		call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})
		return id
	}

	t.Run("a confirmed takeover binds its plan, which a renewal does not change", func(t *testing.T) {
		item := active("plans")
		plan := func(client string) *v1.ControlPlan {
			t.Helper()
			value, err := f.Clients.Control.GetControl(t.Context(), controlAs(&v1.GetControlRequest{ItemId: item, ClientId: client}, client, ""))
			if err != nil {
				t.Fatal(err)
			}
			if value.Msg.Control.Plan.PlanToken == "" {
				t.Fatal("missing plan token")
			}
			return value.Msg.Control.Plan
		}
		acquire := func(client, token string, confirm bool) (*connect.Response[v1.AcquireControlResponse], error) {
			return f.Clients.Control.AcquireControl(t.Context(), controlAs(&v1.AcquireControlRequest{ItemId: item, ClientId: client, Confirm: confirm, PlanToken: token}, client, ""))
		}
		alpha, err := acquire("alpha", "", false)
		if err != nil {
			t.Fatal(err)
		}
		beta := plan("beta")
		if !beta.NeedsConfirmation || beta.Displaces.GetClientId() != "user:operator/alpha" {
			t.Fatal(beta)
		}
		if _, err := acquire("beta", "", true); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("missing token: %v", err)
		}
		if _, err := f.Clients.Control.RenewControl(t.Context(), controlAs(&v1.RenewControlRequest{LeaseId: alpha.Msg.Control.Lease.Id}, "alpha", "")); err != nil {
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
		if _, err := f.Clients.Control.ReleaseControl(t.Context(), controlAs(&v1.ReleaseControlRequest{LeaseId: taken.Msg.Control.Lease.Id}, "beta", "")); err != nil {
			t.Fatal(err)
		}
		if _, err := acquire("alpha", stale.PlanToken, true); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("changed controller accepted: %v", err)
		}
		if _, err := acquire("alpha", plan("alpha").PlanToken, true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("refusals carry the control status", func(t *testing.T) {
		item := active("refusals")
		alpha, err := f.Clients.Control.AcquireControl(t.Context(), controlAs(&v1.AcquireControlRequest{ItemId: item, ClientId: "alpha"}, "alpha", ""))
		if err != nil {
			t.Fatal(err)
		}
		// Taking control from alpha needs confirmation.
		_, err = f.Clients.Control.AcquireControl(t.Context(), controlAs(&v1.AcquireControlRequest{ItemId: item, ClientId: "beta"}, "beta", ""))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatal(err)
		}
		if control := controlStatus(t, err); control.Lease.GetId() != alpha.Msg.Control.Lease.Id || control.Yours || !control.Plan.NeedsConfirmation || control.Plan.PlanToken == "" {
			t.Fatalf("confirmation status: %v", control)
		}
		// Abort takes vacant control, and refuses another holder's.
		_, err = f.Clients.Agents.AbortTurn(t.Context(), controlAs(&v1.AbortTurnRequest{ItemId: item}, "beta", ""))
		if connect.CodeOf(err) != connect.CodePermissionDenied || controlStatus(t, err).Lease.GetClientId() != "user:operator/alpha" {
			t.Fatalf("abort by another client: %v", err)
		}
		// Releasing by item works without the lease ID, and only for its holder.
		if _, err := f.Clients.Control.ReleaseControl(t.Context(), controlAs(&v1.ReleaseControlRequest{ItemId: item}, "beta", "")); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("release by another client: %v", err)
		}
		if _, err := f.Clients.Control.ReleaseControl(t.Context(), controlAs(&v1.ReleaseControlRequest{ItemId: item}, "alpha", "")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Clients.Agents.AbortTurn(t.Context(), controlAs(&v1.AbortTurnRequest{ItemId: item}, "beta", "")); err != nil {
			t.Fatalf("abort with vacant control: %v", err)
		}
		now, err := f.Clients.Control.GetControl(t.Context(), controlAs(&v1.GetControlRequest{ItemId: item, ClientId: "beta"}, "beta", ""))
		if err != nil || !now.Msg.Control.Yours {
			t.Fatalf("abort did not take vacant control: %v %v", now, err)
		}
	})

	// A control lease is its principal's client's: another principal naming
	// the same client, as every command line does by default, holds none of it.
	t.Run("a lease belongs to its principal", func(t *testing.T) {
		item := active("lease")
		mine, err := f.Clients.Control.AcquireControl(t.Context(), controlAs(&v1.AcquireControlRequest{ItemId: item}, "", model.LeaseHolderCLI))
		if err != nil {
			t.Fatal(err)
		}
		leaseID := mine.Msg.Control.Lease.GetId()
		if leaseID == "" || mine.Msg.Control.Lease.ClientId != "user:operator/cli" {
			t.Fatalf("operator lease: %v", mine.Msg.Control.Lease)
		}
		// Another item's agent, which may manage every item by default,
		// names the same client.
		other := newItem(t, f, repository, "other").Id
		agent, _ := f.AgentClients(t, other)
		if _, err := agent.Control.RenewControl(t.Context(), connect.NewRequest(&v1.RenewControlRequest{LeaseId: leaseID})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("renewing another principal's lease: %v", err)
		}
		if _, err := agent.Control.ReleaseControl(t.Context(), connect.NewRequest(&v1.ReleaseControlRequest{ItemId: item})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("releasing another principal's lease: %v", err)
		}
		call(t, f.Clients.Control.RenewControl, &v1.RenewControlRequest{LeaseId: leaseID})
		// What a command line sends takes over what another command line holds.
		if _, err := agent.Agents.AbortTurn(t.Context(), controlAs(&v1.AbortTurnRequest{ItemId: item}, "", model.LeaseHolderCLI)); err != nil {
			t.Fatalf("a command line taking over another's control: %v", err)
		}
		if lease := call(t, f.Clients.Control.GetControl, &v1.GetControlRequest{ItemId: item}).Control.Lease; lease.GetClientId() != "item:"+other+"/cli" {
			t.Fatalf("lease after the handover: %v", lease)
		}
		// A browser tab keeps what it holds.
		plan, err := f.Clients.Control.GetControl(t.Context(), controlAs(&v1.GetControlRequest{ItemId: item, ClientId: "tab"}, "tab", "web"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Clients.Control.AcquireControl(t.Context(), controlAs(&v1.AcquireControlRequest{ItemId: item, ClientId: "tab", Confirm: true, PlanToken: plan.Msg.Control.Plan.PlanToken}, "tab", "web")); err != nil {
			t.Fatal(err)
		}
		if _, err := agent.Agents.AbortTurn(t.Context(), controlAs(&v1.AbortTurnRequest{ItemId: item}, "", model.LeaseHolderCLI)); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("a command line taking a tab's control: %v", err)
		}
	})

	t.Run("a message that requires an online agent starts none", func(t *testing.T) {
		item := active("offline control")
		call(t, f.Clients.Workspaces.EnsureWorkspace, &v1.EnsureWorkspaceRequest{ItemId: item, SkipSetup: true})
		refused(t, connect.CodeFailedPrecondition, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "must not start", RequireOnline: true})
		if value := call(t, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: item}); value.Item.Conversation != nil {
			t.Fatalf("created a conversation: %v", value.Item.Conversation)
		}
	})
}
