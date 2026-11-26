package daemonapi_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"google.golang.org/protobuf/proto"
)

// lifecycleWait waits for a porcelain response's job, when it has one.
func lifecycleWait(t testing.TB, f *apitest.Fixture, job *v1.Job) {
	t.Helper()
	if job != nil {
		apitest.Wait(t, f.Clients, job)
	}
}

// lifecycleSettled waits until item's agent is idle and the review capture
// its last turn started has finished. That capture writes the item, and an
// unfenced porcelain request racing it can be refused as stale (reported as
// a production issue).
func lifecycleSettled(t testing.TB, f *apitest.Fixture, item string) *v1.Agent {
	t.Helper()
	agent := apitest.WaitIdle(t, f.Clients, item)
	eventually(t, "the review capture to finish", func() bool {
		return !lifecycleItem(t, f, item).Observation.GetCapturing()
	})
	return agent
}

// lifecycleItem reads an item.
func lifecycleItem(t testing.TB, f *apitest.Fixture, id string) *v1.GetItemResponse {
	t.Helper()
	return call(t, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: id, IncludeDescription: true})
}

// The porcelain's refusals and partial outcomes, on a daemon that cannot
// start agents.
func TestLifecycleRefusalsAndPartialOutcomes(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithConfig("[deep_work]\nmax_active = 1\n"))
	repo := f.Repository(t, "test")

	t.Run("a malformed start creates nothing", func(t *testing.T) {
		refused(t, connect.CodeInvalidArgument, f.Clients.Lifecycle.CreateAndStartItem, &v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: repo, Title: "must not exist"}, Start: &v1.StartOptions{SendNothing: true, Prompt: "conflict"}})
		if items := call(t, f.Clients.Items.ListItems, &v1.ListItemsRequest{}).Items; len(items) != 0 {
			t.Fatalf("an invalid request had effects: %v", items)
		}
	})

	// The item and its description are durable before the start; a start
	// that fails keeps them, and its job reports what was done.
	t.Run("a failed start keeps the item and reports a partial job", func(t *testing.T) {
		started := call(t, f.Clients.Lifecycle.CreateAndStartItem, &v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: repo, Title: "Partial", Description: "durable description"}, Start: &v1.StartOptions{Prompt: "separate prompt", Mode: v1.AgentMode_AGENT_MODE_HEADLESS}})
		if started.Item.GetId() == "" || started.Job == nil {
			t.Fatalf("create and start: %v", started)
		}
		var job *v1.Job
		eventually(t, "the start job to end", func() bool {
			job = call(t, f.Clients.Jobs.GetJob, &v1.GetJobRequest{JobId: started.Job.Id}).Job
			return job.State == v1.JobState_JOB_STATE_FAILED || job.State == v1.JobState_JOB_STATE_SUCCEEDED
		})
		if job.State != v1.JobState_JOB_STATE_FAILED || job.Error == "" || job.Partial == nil || job.Partial.ItemId != started.Item.Id {
			t.Fatalf("a start without a runnable agent: %v", job)
		}
		value := lifecycleItem(t, f, started.Item.Id)
		if value.Description != "durable description" || value.Item.State != v1.ItemState_ITEM_STATE_ACTIVE {
			t.Fatalf("the created item: %v", value)
		}
		jobs := call(t, f.Clients.Jobs.ListJobs, &v1.ListJobsRequest{ItemId: started.Item.Id}).Jobs
		if len(jobs) == 0 || jobs[0].Partial == nil {
			t.Fatalf("the item's jobs: %v", jobs)
		}
	})

	// Starting, shelving, closing and reopening refuse a request made
	// against an item that changed since the client read it.
	t.Run("stale revisions are refused", func(t *testing.T) {
		created := newItem(t, f, repo, "stale")
		read := created.Revision
		call(t, f.Clients.Items.AddNote, &v1.AddNoteRequest{ItemId: created.Id, Text: "changed meanwhile"})
		current := call(t, f.Clients.Items.UpdateItem, &v1.UpdateItemRequest{ItemId: created.Id, Title: proto.String("renamed meanwhile")}).Item
		if current.Revision == read {
			t.Fatal("revision did not move")
		}
		refused(t, connect.CodeAborted, f.Clients.Lifecycle.StartItem, &v1.StartItemRequest{ItemId: created.Id, ExpectedRevision: read, Start: &v1.StartOptions{SendNothing: true}})
		refused(t, connect.CodeAborted, f.Clients.Lifecycle.ShelveItem, &v1.ShelveItemRequest{ItemId: created.Id, ExpectedRevision: read})
		refused(t, connect.CodeAborted, f.Clients.Lifecycle.CloseItem, &v1.CloseItemRequest{ItemId: created.Id, Resolution: v1.Resolution_RESOLUTION_ABANDONED, ExpectedRevision: read})
		closed := call(t, f.Clients.Lifecycle.CloseItem, &v1.CloseItemRequest{ItemId: created.Id, Resolution: v1.Resolution_RESOLUTION_ABANDONED, ExpectedRevision: current.Revision})
		lifecycleWait(t, f, closed.Job)
		after := lifecycleItem(t, f, created.Id).Item
		if after.State != v1.ItemState_ITEM_STATE_CLOSED || after.Resolution != v1.Resolution_RESOLUTION_ABANDONED {
			t.Fatalf("closed: %v", after)
		}
		refused(t, connect.CodeAborted, f.Clients.Lifecycle.ReopenItem, &v1.ReopenItemRequest{ItemId: created.Id, ExpectedRevision: read})
		reopened := call(t, f.Clients.Lifecycle.ReopenItem, &v1.ReopenItemRequest{ItemId: created.Id, ExpectedRevision: after.Revision})
		lifecycleWait(t, f, reopened.Job)
		if reopened.Item.State != v1.ItemState_ITEM_STATE_BACKLOG || reopened.Item.Slug == "" {
			t.Fatalf("reopened: %v", reopened.Item)
		}
	})

	// PlanClose reports what closing would do and changes nothing.
	t.Run("plan close", func(t *testing.T) {
		item, checkout := activeItem(t, f, repo, "plan close")
		if err := os.WriteFile(filepath.Join(checkout.Claim.Path, "notes.txt"), []byte("work\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		plan := call(t, f.Clients.Lifecycle.PlanClose, &v1.PlanCloseRequest{ItemId: item.Id})
		if plan.AbortsTurn || plan.AttachedTerminalClients != 0 || plan.CheckoutProblem != "" || !slices.Equal(plan.Commits, []string{"notes.txt"}) {
			t.Fatalf("plan: %v", plan)
		}
		if state := lifecycleItem(t, f, item.Id).Item.State; state != v1.ItemState_ITEM_STATE_ACTIVE {
			t.Fatalf("planning changed the item: %v", state)
		}
		// Shelving keeps uncommitted work: its release is refused, and the
		// item stays active with its checkout.
		shelve := call(t, f.Clients.Lifecycle.ShelveItem, &v1.ShelveItemRequest{ItemId: item.Id})
		var job *v1.Job
		eventually(t, "the shelve job to end", func() bool {
			job = call(t, f.Clients.Jobs.GetJob, &v1.GetJobRequest{JobId: shelve.Job.Id}).Job
			return job.State == v1.JobState_JOB_STATE_FAILED || job.State == v1.JobState_JOB_STATE_SUCCEEDED
		})
		if kept := lifecycleItem(t, f, item.Id).Item; job.State != v1.JobState_JOB_STATE_FAILED || kept.State != v1.ItemState_ITEM_STATE_ACTIVE || kept.Checkout.GetClaim() == nil {
			t.Fatalf("shelving uncommitted work: %v %v", job, kept)
		}
		// Force: an item whose agent never ran has no observed agent status,
		// which committing reads as busy (reported as a production issue).
		closed := call(t, f.Clients.Lifecycle.CloseItem, &v1.CloseItemRequest{ItemId: item.Id, Resolution: v1.Resolution_RESOLUTION_COMPLETED, CommitChanges: true, Force: true})
		lifecycleWait(t, f, closed.Job)
		after := lifecycleItem(t, f, item.Id).Item
		if after.State != v1.ItemState_ITEM_STATE_CLOSED || after.Checkout.GetClaim() != nil {
			t.Fatalf("closed with its changes: %v", after)
		}
		// The committed work is on the item's branch: a new checkout has it.
		call(t, f.Clients.Lifecycle.ReopenItem, &v1.ReopenItemRequest{ItemId: item.Id})
		call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: item.Id, State: v1.ItemState_ITEM_STATE_ACTIVE})
		ensured := call(t, f.Clients.Workspaces.EnsureWorkspace, &v1.EnsureWorkspaceRequest{ItemId: item.Id, SkipSetup: true})
		lifecycleWait(t, f, ensured.Job)
		if data, err := os.ReadFile(filepath.Join(ensured.Checkout.Claim.Path, "notes.txt")); err != nil || string(data) != "work\n" {
			t.Fatalf("the committed file: %q %v", data, err)
		}
		if status := git(t, ensured.Checkout.Claim.Path, "status", "--porcelain"); status != "" {
			t.Fatalf("the new checkout is dirty: %s", status)
		}
	})

	t.Run("deep work honors the daemon's capacity", func(t *testing.T) {
		first := call(t, f.Clients.Items.CreateItem, &v1.CreateItemRequest{RepositoryId: repo, Title: "deep one", DeepWork: true}).Item
		second := call(t, f.Clients.Items.CreateItem, &v1.CreateItemRequest{RepositoryId: repo, Title: "deep two", DeepWork: true}).Item
		call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: first.Id, State: v1.ItemState_ITEM_STATE_ACTIVE})
		refused(t, connect.CodeFailedPrecondition, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: second.Id, State: v1.ItemState_ITEM_STATE_ACTIVE})
		refused(t, connect.CodeFailedPrecondition, f.Clients.Lifecycle.StartItem, &v1.StartItemRequest{ItemId: second.Id, Start: &v1.StartOptions{SendNothing: true}})
		call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: second.Id, State: v1.ItemState_ITEM_STATE_ACTIVE, OverrideCapacity: true})
	})
}

// The porcelain with running agents: an item started with a prompt is
// answered, shelved, started again, closed and reopened, each through the
// plumbing it documents.
func TestLifecycleWithAnAgent(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	repo := f.Repository(t, "test")
	started := call(t, f.Clients.Lifecycle.CreateAndStartItem, &v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: repo, Title: "agent work"}, Start: &v1.StartOptions{Prompt: "first prompt", Mode: v1.AgentMode_AGENT_MODE_HEADLESS}})
	item := started.Item.Id
	job := apitest.Wait(t, f.Clients, started.Job)
	history := apitest.WaitHistory(t, f.Clients, item, 2)
	first := lifecycleSettled(t, f, item)
	conversation := lifecycleItem(t, f, item).Item.Conversation.GetId()
	if result := job.GetAgentStart(); result.GetGeneration() != first.Generation || result.GetMode() != v1.AgentMode_AGENT_MODE_HEADLESS {
		t.Fatalf("start result %v, agent %v", job, first)
	}
	if messageText(history[0]) != "first prompt" || !strings.Contains(messageText(history[1]), "Echo: first prompt") {
		t.Fatalf("history: %v", history)
	}

	t.Run("shelve stops the agent and releases the checkout", func(t *testing.T) {
		shelved := call(t, f.Clients.Lifecycle.ShelveItem, &v1.ShelveItemRequest{ItemId: item})
		lifecycleWait(t, f, shelved.Job)
		after := lifecycleItem(t, f, item).Item
		if after.State != v1.ItemState_ITEM_STATE_BACKLOG || after.Checkout.GetClaim() != nil {
			t.Fatalf("shelved: %v", after)
		}
		if agent := call(t, f.Clients.Agents.GetAgent, &v1.GetAgentRequest{ItemId: item}).Agent; agent.Online {
			t.Fatalf("a shelved item's agent runs: %v", agent)
		}
	})

	t.Run("start resumes the conversation in a new generation", func(t *testing.T) {
		restarted := call(t, f.Clients.Lifecycle.StartItem, &v1.StartItemRequest{ItemId: item, Start: &v1.StartOptions{Prompt: "second prompt", Mode: v1.AgentMode_AGENT_MODE_HEADLESS}})
		lifecycleWait(t, f, restarted.Job)
		// The fake driver starts each runtime on a new branch of the
		// conversation, so its history shows the latest turn only.
		var history []*v1.ConversationMessage
		eventually(t, "the second answer", func() bool {
			history = call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: item}).Messages
			return len(history) > 0 && messageText(history[len(history)-1]) == "Echo: second prompt"
		})
		second := lifecycleSettled(t, f, item)
		if second.Generation <= first.Generation || conversation == "" || lifecycleItem(t, f, item).Item.Conversation.GetId() != conversation {
			t.Fatalf("restarted agent %v in another conversation", second)
		}
	})

	t.Run("close stops the agent; reopen with start resumes it", func(t *testing.T) {
		closed := call(t, f.Clients.Lifecycle.CloseItem, &v1.CloseItemRequest{ItemId: item, Resolution: v1.Resolution_RESOLUTION_COMPLETED})
		lifecycleWait(t, f, closed.Job)
		after := lifecycleItem(t, f, item).Item
		if after.State != v1.ItemState_ITEM_STATE_CLOSED || after.Checkout.GetClaim() != nil {
			t.Fatalf("closed: %v", after)
		}
		if agent := call(t, f.Clients.Agents.GetAgent, &v1.GetAgentRequest{ItemId: item}).Agent; agent.Online {
			t.Fatalf("a closed item's agent runs: %v", agent)
		}
		reopened := call(t, f.Clients.Lifecycle.ReopenItem, &v1.ReopenItemRequest{ItemId: item, Start: true, Mode: v1.AgentMode_AGENT_MODE_HEADLESS})
		lifecycleWait(t, f, reopened.Job)
		agent := apitest.WaitIdle(t, f.Clients, item)
		value := lifecycleItem(t, f, item).Item
		if value.State != v1.ItemState_ITEM_STATE_ACTIVE || value.Checkout.GetBranch() != after.Checkout.Branch || agent.Generation <= first.Generation {
			t.Fatalf("reopened: %v %v", value, agent)
		}
		if history := call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: item}).Messages; len(history) == 0 || messageText(history[len(history)-1]) != "Echo: second prompt" {
			t.Fatalf("reopening sent a message: %v", history)
		}
	})
}
