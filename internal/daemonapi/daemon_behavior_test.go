package daemonapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestListSectionFollowsAttentionThenProblems(t *testing.T) {
	problem := &v1.ItemObservation{AgentStatus: v1.AgentStatus_AGENT_STATUS_PROBLEM}
	checkout := &v1.ItemObservation{WorktreeStatus: v1.WorktreeStatus_WORKTREE_STATUS_PROBLEM}
	for _, test := range []struct {
		group v1.AttentionGroup
		o     *v1.ItemObservation
		want  v1.ListSection
	}{
		{v1.AttentionGroup_ATTENTION_GROUP_CLOSED, problem, v1.ListSection_LIST_SECTION_CLOSED},
		{v1.AttentionGroup_ATTENTION_GROUP_BACKLOG, problem, v1.ListSection_LIST_SECTION_BACKLOG},
		// A usage-limited agent is a problem, but its item waits.
		{v1.AttentionGroup_ATTENTION_GROUP_WAITING, problem, v1.ListSection_LIST_SECTION_WAITING},
		{v1.AttentionGroup_ATTENTION_GROUP_WORKING, checkout, v1.ListSection_LIST_SECTION_NEEDS_FIXING},
		{v1.AttentionGroup_ATTENTION_GROUP_NEEDS_ATTENTION, problem, v1.ListSection_LIST_SECTION_NEEDS_FIXING},
		{v1.AttentionGroup_ATTENTION_GROUP_WORKING, &v1.ItemObservation{}, v1.ListSection_LIST_SECTION_IN_PROGRESS},
		{v1.AttentionGroup_ATTENTION_GROUP_NEEDS_ATTENTION, nil, v1.ListSection_LIST_SECTION_NEEDS_ATTENTION},
	} {
		if got := listSection(&v1.ItemSummary{AttentionGroup: test.group, Observation: test.o}); got != test.want {
			t.Errorf("%v %v: got %v, want %v", test.group, test.o, got, test.want)
		}
	}
}

// Sending to a backlog item starts it, with the message (and its images) as
// the first prompt; an invalid image is refused before anything starts.
func TestSendingToABacklogItemStartsIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.repo(t), Title: "send starts"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	bad := &v1.Image{MimeType: "text/plain", Data: []byte("x")}
	if _, err := f.clients.Agents.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: id, Text: "go", Images: []*v1.Image{bad}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid image: %v", err)
	}
	if _, err := f.clients.Lifecycle.StartItem(ctx, connect.NewRequest(&v1.StartItemRequest{ItemId: id, Start: &v1.StartOptions{Images: []*v1.Image{bad}}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid start image: %v", err)
	}
	item, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
	if err != nil || item.Msg.Item.State != v1.ItemState_ITEM_STATE_BACKLOG {
		t.Fatalf("refused send changed the item: %v %v", item, err)
	}
	png := &v1.Image{MimeType: "image/png", Data: []byte("\x89PNG\r\n\x1a\n")}
	sent, err := f.clients.Agents.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: id, Text: "go", Images: []*v1.Image{png}, ClientMessageId: "m1"}))
	if err != nil {
		t.Fatal(err)
	}
	if sent.Msg.Job == nil || sent.Msg.Job.Kind != "start_item" {
		t.Fatalf("send did not start the item: %v", sent.Msg)
	}
	item, err = f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
	if err != nil || item.Msg.Item.State != v1.ItemState_ITEM_STATE_ACTIVE {
		t.Fatalf("item not active: %v %v", item, err)
	}
}

// A refused control call carries the item's control status, so the client
// can show who holds control without asking again.
func TestControlRefusalsCarryTheControlStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.repo(t), Title: "refusals"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	if _, err := f.clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	acquire := connect.NewRequest(&v1.AcquireControlRequest{ItemId: id, ClientId: "alpha"})
	acquire.Header().Set(ClientHeader, "alpha")
	alpha, err := f.clients.Control.AcquireControl(ctx, acquire)
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
	take.Header().Set(ClientHeader, "beta")
	_, err = f.clients.Control.AcquireControl(ctx, take)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatal(err)
	}
	if control := status(err); control.Lease.GetId() != alpha.Msg.Control.Lease.Id || control.Yours || !control.Plan.NeedsConfirmation || control.Plan.PlanToken == "" {
		t.Fatalf("confirmation status: %v", control)
	}
	// Abort and settings take vacant control, and refuse another holder's.
	abort := connect.NewRequest(&v1.AbortTurnRequest{ItemId: id})
	abort.Header().Set(ClientHeader, "beta")
	_, err = f.clients.Agents.AbortTurn(ctx, abort)
	if connect.CodeOf(err) != connect.CodePermissionDenied || status(err).Lease.GetClientId() != "alpha" {
		t.Fatalf("abort by another client: %v", err)
	}
	// Releasing by item works without the lease ID, and only for its holder.
	release := connect.NewRequest(&v1.ReleaseControlRequest{ItemId: id})
	release.Header().Set(ClientHeader, "beta")
	if _, err := f.clients.Control.ReleaseControl(ctx, release); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("release by another client: %v", err)
	}
	release = connect.NewRequest(&v1.ReleaseControlRequest{ItemId: id})
	release.Header().Set(ClientHeader, "alpha")
	if _, err := f.clients.Control.ReleaseControl(ctx, release); err != nil {
		t.Fatal(err)
	}
	abort = connect.NewRequest(&v1.AbortTurnRequest{ItemId: id})
	abort.Header().Set(ClientHeader, "beta")
	if _, err := f.clients.Agents.AbortTurn(ctx, abort); err != nil {
		t.Fatalf("abort with vacant control: %v", err)
	}
	get := connect.NewRequest(&v1.GetControlRequest{ItemId: id, ClientId: "beta"})
	get.Header().Set(ClientHeader, "beta")
	now, err := f.clients.Control.GetControl(ctx, get)
	if err != nil || !now.Msg.Control.Yours {
		t.Fatalf("abort did not take vacant control: %v %v", now, err)
	}
}

// A failed start leaves no agent to observe: the item shows the failure as
// an agent problem until the next start.
func TestAFailedStartIsAnAgentProblem(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.repo(t), Title: "failed start"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	if _, err := f.clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	get := func() *v1.ItemObservation {
		t.Helper()
		value, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Observation
	}
	finish := func(kind string, fail error) {
		t.Helper()
		job := f.api.jobs.start(kind, id, "", func(context.Context, *jobRun) error { return fail })
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			value, _ := f.api.jobs.get(job.Id)
			if snapshot, _ := value.snapshot(); terminalJob(snapshot.State) {
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
