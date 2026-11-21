package daemonapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// While a job starts an item's agent, the item reads busy with the job's
// phase, and change readers learn when it starts and ends: before the
// runtime exists, nothing else knows the agent is coming.
func TestAJobStartingTheAgentShowsTheItemStarting(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.repo(t), Title: "starting"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	get := func() *v1.ItemObservation {
		t.Helper()
		value, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Observation
	}
	if o := get(); o.AgentStatus == v1.AgentStatus_AGENT_STATUS_BUSY {
		t.Fatalf("busy before any start: %v", o)
	}
	changes := 0
	f.api.jobs.itemChanged = func(item string) {
		if item == id {
			changes++
		}
	}
	release := make(chan struct{})
	phased := make(chan struct{})
	job := f.api.jobs.start("start_agent", id, "", func(ctx context.Context, j *jobRun) error {
		j.change(func(v *v1.Job) { v.Phase = "setting up the checkout" })
		close(phased)
		<-release
		return nil
	})
	<-phased
	if o := get(); o.AgentStatus != v1.AgentStatus_AGENT_STATUS_BUSY || !strings.Contains(o.AgentReason, "setting up the checkout") || o.NeedsAttention {
		t.Fatalf("starting: %v", o)
	}
	close(release)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		value, _ := f.api.jobs.get(job.Id)
		if snapshot, _ := value.snapshot(); terminalJob(snapshot.State) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
	}
	if o := get(); o.AgentStatus == v1.AgentStatus_AGENT_STATUS_BUSY {
		t.Fatalf("still starting after the job: %v", o)
	}
	// Queued, running, its phase and its end each wake change readers.
	if changes < 3 {
		t.Fatalf("change readers woken %d times", changes)
	}
	// Other jobs are not a start.
	hold := make(chan struct{})
	f.api.jobs.start("setup_workspace", id, "", func(context.Context, *jobRun) error { <-hold; return nil })
	defer close(hold)
	if o := get(); o.AgentStatus == v1.AgentStatus_AGENT_STATUS_BUSY {
		t.Fatalf("a checkout setup read as starting: %v", o)
	}
}
