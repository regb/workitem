package daemonapi_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/daemonapi"
	"github.com/regb/workitem/internal/model"
	"google.golang.org/protobuf/proto"
)

// Sending to a backlog item starts it, with the message (and its images) as
// the first prompt; an invalid image is refused before anything starts.
func TestSendingToABacklogItemStartsIt(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.Repository(t, "test"), Title: "send starts"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	bad := &v1.Image{MimeType: "text/plain", Data: []byte("x")}
	if _, err := f.Clients.Agents.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: id, Text: "go", Images: []*v1.Image{bad}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid image: %v", err)
	}
	if _, err := f.Clients.Lifecycle.StartItem(ctx, connect.NewRequest(&v1.StartItemRequest{ItemId: id, Start: &v1.StartOptions{Images: []*v1.Image{bad}}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid start image: %v", err)
	}
	item, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
	if err != nil || item.Msg.Item.State != v1.ItemState_ITEM_STATE_BACKLOG {
		t.Fatalf("refused send changed the item: %v %v", item, err)
	}
	png := &v1.Image{MimeType: "image/png", Data: []byte("\x89PNG\r\n\x1a\n")}
	sent, err := f.Clients.Agents.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: id, Text: "go", Images: []*v1.Image{png}, ClientMessageId: "m1"}))
	if err != nil {
		t.Fatal(err)
	}
	if sent.Msg.Job == nil || sent.Msg.Job.Kind != "start_item" {
		t.Fatalf("send did not start the item: %v", sent.Msg)
	}
	item, err = f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
	if err != nil || item.Msg.Item.State != v1.ItemState_ITEM_STATE_ACTIVE {
		t.Fatalf("item not active: %v %v", item, err)
	}
}

// A refused control call carries the item's control status, so the client
// can show who holds control without asking again.
func TestControlRefusalsCarryTheControlStatus(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.Repository(t, "test"), Title: "refusals"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	acquire := connect.NewRequest(&v1.AcquireControlRequest{ItemId: id, ClientId: "alpha"})
	acquire.Header().Set(daemonapi.ClientHeader, "alpha")
	alpha, err := f.Clients.Control.AcquireControl(ctx, acquire)
	if err != nil {
		t.Fatal(err)
	}
	status := func(err error) *v1.ControlStatus {
		t.Helper()
		var refused *connect.Error
		if !errors.As(err, &refused) {
			t.Fatalf("not a refusal: %v", err)
		}
		for _, detail := range refused.Details() {
			if value, err := detail.Value(); err == nil {
				if control, ok := value.(*v1.ControlStatus); ok {
					return control
				}
			}
		}
		t.Fatalf("refusal without control status: %v", err)
		return nil
	}
	// Taking control from alpha needs confirmation.
	take := connect.NewRequest(&v1.AcquireControlRequest{ItemId: id, ClientId: "beta"})
	take.Header().Set(daemonapi.ClientHeader, "beta")
	_, err = f.Clients.Control.AcquireControl(ctx, take)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal(err)
	}
	if control := status(err); control.Lease.GetId() != alpha.Msg.Control.Lease.Id || control.Yours || !control.Plan.NeedsConfirmation || control.Plan.PlanToken == "" {
		t.Fatalf("confirmation status: %v", control)
	}
	// Abort and settings take vacant control, and refuse another holder's.
	abort := connect.NewRequest(&v1.AbortTurnRequest{ItemId: id})
	abort.Header().Set(daemonapi.ClientHeader, "beta")
	_, err = f.Clients.Agents.AbortTurn(ctx, abort)
	if connect.CodeOf(err) != connect.CodePermissionDenied || status(err).Lease.GetClientId() != "user:operator/alpha" {
		t.Fatalf("abort by another client: %v", err)
	}
	// Releasing by item works without the lease ID, and only for its holder.
	release := connect.NewRequest(&v1.ReleaseControlRequest{ItemId: id})
	release.Header().Set(daemonapi.ClientHeader, "beta")
	if _, err := f.Clients.Control.ReleaseControl(ctx, release); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("release by another client: %v", err)
	}
	release = connect.NewRequest(&v1.ReleaseControlRequest{ItemId: id})
	release.Header().Set(daemonapi.ClientHeader, "alpha")
	if _, err := f.Clients.Control.ReleaseControl(ctx, release); err != nil {
		t.Fatal(err)
	}
	abort = connect.NewRequest(&v1.AbortTurnRequest{ItemId: id})
	abort.Header().Set(daemonapi.ClientHeader, "beta")
	if _, err := f.Clients.Agents.AbortTurn(ctx, abort); err != nil {
		t.Fatalf("abort with vacant control: %v", err)
	}
	get := connect.NewRequest(&v1.GetControlRequest{ItemId: id, ClientId: "beta"})
	get.Header().Set(daemonapi.ClientHeader, "beta")
	now, err := f.Clients.Control.GetControl(ctx, get)
	if err != nil || !now.Msg.Control.Yours {
		t.Fatalf("abort did not take vacant control: %v %v", now, err)
	}
}

// A failed start leaves no agent to observe: the item shows the failure as
// an agent problem until the next start.
func TestAFailedStartIsAnAgentProblem(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.Repository(t, "test"), Title: "failed start"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	get := func() *v1.ItemObservation {
		t.Helper()
		value, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Observation
	}
	finish := func(kind string, fail error) {
		t.Helper()
		job := f.API().StartJob(kind, id, "", func(context.Context, *daemonapi.JobRun) error { return fail })
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			snapshot, _ := f.API().JobSnapshot(job.Id)
			if snapshot != nil && daemonapi.TerminalJob(snapshot.State) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("job did not finish")
			}
		}
	}
	finish("start_agent", errors.New("driver exploded"))
	if o := get(); o.AgentStatus != v1.AgentStatus_AGENT_STATUS_PROBLEM || !strings.Contains(o.AgentReason, "driver exploded") || !o.NeedsAttention {
		t.Fatalf("failed start: %v", o)
	}
	// Other failed jobs are not a start.
	finish("setup_workspace", errors.New("setup failed"))
	if o := get(); !strings.Contains(o.AgentReason, "driver exploded") {
		t.Fatalf("a later setup hid the failed start: %v", o)
	}
	finish("start_agent", nil)
	if o := get(); o.AgentStatus == v1.AgentStatus_AGENT_STATUS_PROBLEM {
		t.Fatalf("problem after a successful start: %v", o)
	}
}

// A close plan says what closing would do now, changing nothing.
func TestPlanCloseReportsWhatClosingWouldDo(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.Repository(t, "test"), Title: "plan close"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	workspace, err := f.Clients.Workspaces.EnsureWorkspace(ctx, connect.NewRequest(&v1.EnsureWorkspaceRequest{ItemId: id, SkipSetup: true}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Msg.Checkout.Claim.Path, "notes.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := f.Clients.Lifecycle.PlanClose(ctx, connect.NewRequest(&v1.PlanCloseRequest{ItemId: id}))
	if err != nil {
		t.Fatal(err)
	}
	if p := plan.Msg; p.AbortsTurn || p.AttachedTerminalClients != 0 || p.CheckoutProblem != "" || len(p.Commits) != 1 || p.Commits[0] != "notes.txt" {
		t.Fatalf("plan: %v", p)
	}
	item, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
	if err != nil || item.Msg.Item.State != v1.ItemState_ITEM_STATE_ACTIVE {
		t.Fatalf("planning changed the item: %v %v", item, err)
	}
}

// Shelving, closing and reopening refuse a request made against an item
// that changed since the client read it, as starting does.
func TestLifecyclePorcelainRefusesStaleRevisions(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.Repository(t, "test"), Title: "stale"}))
	if err != nil {
		t.Fatal(err)
	}
	id, read := created.Msg.Item.Id, created.Msg.Item.Revision
	if _, err := f.Clients.Items.AddNote(ctx, connect.NewRequest(&v1.AddNoteRequest{ItemId: id, Text: "changed meanwhile"})); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Clients.Items.UpdateItem(ctx, connect.NewRequest(&v1.UpdateItemRequest{ItemId: id, Title: proto.String("renamed meanwhile")})); err != nil {
		t.Fatal(err)
	}
	current, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
	if err != nil || current.Msg.Item.Revision == read {
		t.Fatalf("revision did not move: %v %v", current, err)
	}
	if _, err := f.Clients.Lifecycle.ShelveItem(ctx, connect.NewRequest(&v1.ShelveItemRequest{ItemId: id, ExpectedRevision: read})); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale shelve: %v", err)
	}
	if _, err := f.Clients.Lifecycle.CloseItem(ctx, connect.NewRequest(&v1.CloseItemRequest{ItemId: id, Resolution: v1.Resolution_RESOLUTION_ABANDONED, ExpectedRevision: read})); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale close: %v", err)
	}
	closed, err := f.Clients.Lifecycle.CloseItem(ctx, connect.NewRequest(&v1.CloseItemRequest{ItemId: id, Resolution: v1.Resolution_RESOLUTION_ABANDONED, ExpectedRevision: current.Msg.Item.Revision}))
	if err != nil {
		t.Fatal(err)
	}
	if closed.Msg.Job != nil {
		apitest.Wait(t, f.Clients, closed.Msg.Job)
	}
	after, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
	if err != nil || after.Msg.Item.State != v1.ItemState_ITEM_STATE_CLOSED {
		t.Fatalf("not closed: %v %v", after, err)
	}
	if _, err := f.Clients.Lifecycle.ReopenItem(ctx, connect.NewRequest(&v1.ReopenItemRequest{ItemId: id, ExpectedRevision: read})); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale reopen: %v", err)
	}
	if _, err := f.Clients.Lifecycle.ReopenItem(ctx, connect.NewRequest(&v1.ReopenItemRequest{ItemId: id, ExpectedRevision: after.Msg.Item.Revision})); err != nil {
		t.Fatal(err)
	}
}

// A control lease is its principal's client's: another principal naming
// the same client, as every command line does by default, holds none of it.
func TestAControlLeaseBelongsToItsPrincipal(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	repo := f.Repository(t, "test")
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "lease"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	asCLI := func(req interface{ Header() http.Header }) {
		req.Header().Set(daemonapi.HolderHeader, model.LeaseHolderCLI)
	}
	acquire := connect.NewRequest(&v1.AcquireControlRequest{ItemId: id})
	asCLI(acquire)
	mine, err := f.Clients.Control.AcquireControl(ctx, acquire)
	if err != nil {
		t.Fatal(err)
	}
	leaseID := mine.Msg.Control.Lease.GetId()
	if leaseID == "" || mine.Msg.Control.Lease.ClientId != "user:operator/cli" {
		t.Fatalf("operator lease: %v", mine.Msg.Control.Lease)
	}
	// Another item's agent, which may manage every item by default, names
	// the same client.
	other, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "other"}))
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := f.AgentClients(t, other.Msg.Item.Id)
	if _, err := agent.Control.RenewControl(ctx, connect.NewRequest(&v1.RenewControlRequest{LeaseId: leaseID})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("renewing another principal's lease: %v", err)
	}
	if _, err := agent.Control.ReleaseControl(ctx, connect.NewRequest(&v1.ReleaseControlRequest{ItemId: id})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("releasing another principal's lease: %v", err)
	}
	if _, err := f.Clients.Control.RenewControl(ctx, connect.NewRequest(&v1.RenewControlRequest{LeaseId: leaseID})); err != nil {
		t.Fatalf("renewing one's own lease: %v", err)
	}
	// What a command line sends takes over what another command line holds.
	abort := connect.NewRequest(&v1.AbortTurnRequest{ItemId: id})
	asCLI(abort)
	if _, err := agent.Agents.AbortTurn(ctx, abort); err != nil {
		t.Fatalf("a command line taking over another's control: %v", err)
	}
	if current, err := f.API().Local.Lease(ctx, id); err != nil || current == nil || current.ClientID != "item:"+other.Msg.Item.Id+"/cli" {
		t.Fatalf("lease after the handover: %+v %v", current, err)
	}
	// A browser tab keeps what it holds.
	tab := connect.NewRequest(&v1.AcquireControlRequest{ItemId: id, ClientId: "tab", Confirm: true})
	tab.Header().Set(daemonapi.ClientHeader, "tab")
	tab.Header().Set(daemonapi.HolderHeader, "web")
	view := connect.NewRequest(&v1.GetControlRequest{ItemId: id, ClientId: "tab"})
	view.Header().Set(daemonapi.ClientHeader, "tab")
	view.Header().Set(daemonapi.HolderHeader, "web")
	plan, err := f.Clients.Control.GetControl(ctx, view)
	if err != nil {
		t.Fatal(err)
	}
	tab.Msg.PlanToken = plan.Msg.Control.Plan.PlanToken
	if _, err := f.Clients.Control.AcquireControl(ctx, tab); err != nil {
		t.Fatal(err)
	}
	abort = connect.NewRequest(&v1.AbortTurnRequest{ItemId: id})
	asCLI(abort)
	if _, err := agent.Agents.AbortTurn(ctx, abort); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a command line taking a tab's control: %v", err)
	}
}
