package daemonapi_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/daemonapi"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// agentsGet is item's agent.
func agentsGet(t testing.TB, f *apitest.Fixture, item string) *v1.Agent {
	t.Helper()
	return call(t, f.Clients.Agents.GetAgent, &v1.GetAgentRequest{ItemId: item}).Agent
}

// agentsItem is item, with its observation.
func agentsItem(t testing.TB, f *apitest.Fixture, item string) *v1.GetItemResponse {
	t.Helper()
	return call(t, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: item})
}

// agentsLast is the last message of item's history.
func agentsLast(t testing.TB, f *apitest.Fixture, item string) *v1.ConversationMessage {
	t.Helper()
	messages := call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: item}).Messages
	if len(messages) == 0 {
		return &v1.ConversationMessage{}
	}
	return messages[len(messages)-1]
}

// agentsLastPrompt is the text of the last user message of item's history.
func agentsLastPrompt(t testing.TB, f *apitest.Fixture, item string) string {
	t.Helper()
	text := ""
	for _, message := range call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: item}).Messages {
		if message.Role == v1.MessageRole_MESSAGE_ROLE_USER {
			text = messageText(message)
		}
	}
	return text
}

// agentsSnapshot is the first snapshot of item's live content.
func agentsSnapshot(t testing.TB, f *apitest.Fixture, item string) *v1.LiveContent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := f.Clients.Agents.WatchLiveContent(ctx, connect.NewRequest(&v1.WatchLiveContentRequest{ItemId: item}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() || stream.Msg().GetSnapshot() == nil {
		t.Fatalf("live content snapshot: %v %v", stream.Msg(), stream.Err())
	}
	return stream.Msg().GetSnapshot()
}

// One agent, driven through its life: messages of every delivery, live
// content, settings, a question, abort, restart with its generation fence,
// stop, and a message that starts it again.
func TestAgentsRunConverseAndRestart(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	item, agent := startedAgent(t, f)

	t.Run("start", func(t *testing.T) {
		if !agent.Online || agent.Mode != v1.AgentMode_AGENT_MODE_HEADLESS || agent.Generation == 0 || agent.Driver != "pi" {
			t.Fatalf("started agent: %v", agent)
		}
		if c := agent.Capabilities; c == nil || !c.Prompt || !c.Steering || !c.FollowUp || !c.Cancel || !c.Answer || !c.LiveContent {
			t.Fatalf("capabilities: %v", agent.Capabilities)
		}
	})

	t.Run("deliveries", func(t *testing.T) {
		sent := call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "hello", Delivery: v1.Delivery_DELIVERY_PROMPT, ExpectedGeneration: agent.Generation, ClientMessageId: "hello-1"})
		if sent.Generation != agent.Generation {
			t.Fatalf("sent to generation %d, not %d", sent.Generation, agent.Generation)
		}
		apitest.WaitHistory(t, f.Clients, item, 2)
		apitest.WaitIdle(t, f.Clients, item)
		if last := agentsLast(t, f, item); last.Role != v1.MessageRole_MESSAGE_ROLE_ASSISTANT || messageText(last) != "Echo: hello" {
			t.Fatalf("the agent's answer: %v", last)
		}
		// A retry of the same message is delivered once.
		call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "hello", Delivery: v1.Delivery_DELIVERY_PROMPT, ClientMessageId: "hello-1"})
		if count := len(call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: item}).Messages); count != 2 {
			t.Fatalf("a retried message was delivered again: %d messages", count)
		}
		refused(t, connect.CodeAborted, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "stale", ExpectedGeneration: agent.Generation + 1})
		// Steering and follow-ups reach a busy agent.
		call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "hold on", Delivery: v1.Delivery_DELIVERY_PROMPT})
		eventually(t, "a busy agent", func() bool { return agentsGet(t, f, item).Status == v1.AgentStatus_AGENT_STATUS_BUSY })
		call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "steered", Delivery: v1.Delivery_DELIVERY_STEER})
		eventually(t, "the steered answer", func() bool { return messageText(agentsLast(t, f, item)) == "Echo: steered" })
		call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "followed", Delivery: v1.Delivery_DELIVERY_FOLLOW_UP})
		eventually(t, "the follow-up's answer", func() bool { return messageText(agentsLast(t, f, item)) == "Echo: followed" })
		apitest.WaitIdle(t, f.Clients, item)
	})

	t.Run("abort", func(t *testing.T) {
		call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "hold this turn"})
		eventually(t, "a busy agent", func() bool { return agentsGet(t, f, item).Status == v1.AgentStatus_AGENT_STATUS_BUSY })
		call(t, f.Clients.Agents.AbortTurn, &v1.AbortTurnRequest{ItemId: item})
		apitest.WaitIdle(t, f.Clients, item)
		if prompt := agentsLastPrompt(t, f, item); prompt != "hold this turn" {
			t.Fatalf("last prompt: %q", prompt)
		}
	})

	t.Run("live content", func(t *testing.T) {
		if snapshot := agentsSnapshot(t, f, item); snapshot.Generation != agent.Generation {
			t.Fatalf("snapshot of generation %d, not %d", snapshot.Generation, agent.Generation)
		}
		// A subscription does not affect the agent's lifetime.
		if current := apitest.WaitIdle(t, f.Clients, item); current.Generation != agent.Generation {
			t.Fatalf("watching replaced the agent: %v", current)
		}
	})

	t.Run("settings", func(t *testing.T) {
		before := call(t, f.Clients.Agents.GetAgentSettings, &v1.GetAgentSettingsRequest{ItemId: item})
		if before.Driver != "pi" || before.Catalog == nil || len(before.Catalog.Models) == 0 || before.Running["model"] != "test/model" {
			t.Fatalf("settings: %v", before)
		}
		// A live change reaches the running agent without restarting it.
		call(t, f.Clients.Agents.UpdateAgentSettings, &v1.UpdateAgentSettingsRequest{ItemId: item, Settings: map[string]string{"model": "test/model", "reasoning": "low"}})
		eventually(t, "the running agent's reasoning", func() bool {
			return call(t, f.Clients.Agents.GetAgentSettings, &v1.GetAgentSettingsRequest{ItemId: item}).Running["reasoning"] == "low"
		})
		after := call(t, f.Clients.Agents.GetAgentSettings, &v1.GetAgentSettingsRequest{ItemId: item})
		if after.Overrides["reasoning"] != "low" {
			t.Fatalf("overrides: %v", after.Overrides)
		}
		if current := apitest.WaitIdle(t, f.Clients, item); current.Generation != agent.Generation {
			t.Fatalf("a live setting restarted the agent: %v", current)
		}
		refused(t, connect.CodeInvalidArgument, f.Clients.Agents.UpdateAgentSettings, &v1.UpdateAgentSettingsRequest{ItemId: item, Settings: map[string]string{"reasoning": "unheard-of"}})
	})

	t.Run("question", func(t *testing.T) {
		if _, err := f.Clients.Agents.AnswerQuestion(t.Context(), connect.NewRequest(&v1.AnswerQuestionRequest{ItemId: item, ToolCallId: "none", Answers: map[string]string{"Which color?": "Red"}})); err == nil {
			t.Fatal("an answer without a question was accepted")
		}
		call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "please ask me something"})
		eventually(t, "a question that needs attention", func() bool {
			o := agentsItem(t, f, item).Observation
			return o.GetAsking() && o.GetNeedsAttention()
		})
		question := agentsSnapshot(t, f, item).Question
		if question == nil || question.Id == "" || !strings.Contains(question.Input, "Which color?") {
			t.Fatalf("pending question: %v", question)
		}
		call(t, f.Clients.Agents.AnswerQuestion, &v1.AnswerQuestionRequest{ItemId: item, ToolCallId: question.Id, Answers: map[string]string{"Which color?": "Blue"}})
		eventually(t, "the agent to go on with the answer", func() bool { return messageText(agentsLast(t, f, item)) == "Chosen: Blue" })
		apitest.WaitIdle(t, f.Clients, item)
		if _, err := f.Clients.Agents.AnswerQuestion(t.Context(), connect.NewRequest(&v1.AnswerQuestionRequest{ItemId: item, ToolCallId: question.Id, Answers: map[string]string{"Which color?": "Red"}})); err == nil {
			t.Fatal("a question was answered twice")
		}
		if o := agentsItem(t, f, item).Observation; o.Asking {
			t.Fatalf("still asking: %v", o)
		}
	})

	t.Run("history", func(t *testing.T) {
		history := call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: item})
		if len(history.Messages) < 10 || history.LeafId == "" {
			t.Fatalf("history: %d messages, leaf %q", len(history.Messages), history.LeafId)
		}
		// The test driver's conversations have no subagents to read.
		if _, err := f.Clients.Agents.GetSubagentHistory(t.Context(), connect.NewRequest(&v1.GetSubagentHistoryRequest{ItemId: item, SubagentId: "task-1"})); err == nil || !strings.Contains(err.Error(), "subagent history") {
			t.Fatalf("subagent history of a driver without subagents: %v", err)
		}
	})

	var restarted *v1.Agent
	t.Run("restart", func(t *testing.T) {
		refused(t, connect.CodeAborted, f.Clients.Agents.RestartAgent, &v1.RestartAgentRequest{ItemId: item, ExpectedGeneration: agent.Generation + 1})
		if current := apitest.WaitIdle(t, f.Clients, item); current.Generation != agent.Generation {
			t.Fatalf("a refused restart replaced the agent: %d -> %d", agent.Generation, current.Generation)
		}
		value := call(t, f.Clients.Agents.RestartAgent, &v1.RestartAgentRequest{ItemId: item, ExpectedGeneration: agent.Generation})
		if value.Job != nil {
			apitest.Wait(t, f.Clients, value.Job)
		}
		restarted = apitest.WaitIdle(t, f.Clients, item)
		if restarted.Generation <= agent.Generation {
			t.Fatalf("generation did not advance: %d -> %d", agent.Generation, restarted.Generation)
		}
		refused(t, connect.CodeAborted, f.Clients.Agents.RestartAgent, &v1.RestartAgentRequest{ItemId: item, ExpectedGeneration: agent.Generation})
		// The conversation goes on.
		send(t, f, item, "after the restart")
	})

	t.Run("stop and send to the stopped agent", func(t *testing.T) {
		call(t, f.Clients.Agents.StopAgent, &v1.StopAgentRequest{ItemId: item, Force: true})
		if stopped := agentsGet(t, f, item); stopped.Online {
			t.Fatalf("stopped agent is online: %v", stopped)
		}
		// Nobody controls the stopped agent, as after a scheduled start.
		if lease := call(t, f.Clients.Control.GetControl, &v1.GetControlRequest{ItemId: item}).Control.Lease; lease != nil {
			call(t, f.Clients.Control.ReleaseControl, &v1.ReleaseControlRequest{LeaseId: lease.Id})
		}
		// The start loads the worktree's new .envrc without asking: direnv
		// allows it where the agent's environment keeps allowances.
		if _, err := exec.LookPath("direnv"); err == nil {
			checkout := agentsItem(t, f, item).Item.Checkout.Claim.Path
			if err := os.WriteFile(filepath.Join(checkout, ".envrc"), []byte("export WI_APPROVAL_TEST=1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Log("direnv is not installed: the start loads no .envrc")
		}
		// Sent as the browser sends it: a web client, which takes control.
		request := connect.NewRequest(&v1.SendMessageRequest{ItemId: item, Text: "after the stop", ClientMessageId: "after-stop", StartMode: v1.AgentMode_AGENT_MODE_HEADLESS})
		request.Header().Set(daemonapi.ClientHeader, "browser-1")
		request.Header().Set(daemonapi.HolderHeader, "web")
		sent, err := f.Clients.Agents.SendMessage(t.Context(), request)
		if err != nil || sent.Msg.Job == nil {
			t.Fatalf("send to a stopped agent: %v %v", sent, err)
		}
		apitest.Wait(t, f.Clients, sent.Msg.Job)
		current := apitest.WaitIdle(t, f.Clients, item)
		if current.Generation <= restarted.Generation {
			t.Fatalf("the send did not start a new agent: %v", current)
		}
		eventually(t, "the message to reach the new agent", func() bool { return agentsLastPrompt(t, f, item) == "after the stop" })
	})
}

// Sending to a backlog item starts it, with the message (and its images) as
// the first prompt, which waits for the new agent to be idle; an invalid
// image is refused before anything starts.
func TestAgentsSendingToABacklogItemStartsIt(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	item := newItem(t, f, f.Repository(t, "test"), "send starts").Id
	bad := &v1.Image{MimeType: "text/plain", Data: []byte("x")}
	refused(t, connect.CodeInvalidArgument, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "go", Images: []*v1.Image{bad}})
	refused(t, connect.CodeInvalidArgument, f.Clients.Lifecycle.StartItem, &v1.StartItemRequest{ItemId: item, Start: &v1.StartOptions{Images: []*v1.Image{bad}}})
	if state := agentsItem(t, f, item).Item.State; state != v1.ItemState_ITEM_STATE_BACKLOG {
		t.Fatalf("a refused send changed the item: %v", state)
	}
	png := &v1.Image{MimeType: "image/png", Data: []byte("\x89PNG\r\n\x1a\n")}
	sent := call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "go", Images: []*v1.Image{png}, ClientMessageId: "m1", StartMode: v1.AgentMode_AGENT_MODE_HEADLESS})
	if sent.Job == nil || sent.Job.Kind != "start_item" {
		t.Fatalf("send did not start the item: %v", sent)
	}
	apitest.Wait(t, f.Clients, sent.Job)
	if state := agentsItem(t, f, item).Item.State; state != v1.ItemState_ITEM_STATE_ACTIVE {
		t.Fatalf("item not active: %v", state)
	}
	apitest.WaitHistory(t, f.Clients, item, 2)
	if last := agentsLast(t, f, item); messageText(last) != "Echo: go" {
		t.Fatalf("the first prompt's answer: %v", last)
	}
}

// A failed start leaves no agent to observe: the item shows the failure as
// an agent problem until the next start; other failed jobs do not hide it.
func TestAgentsFailedStartIsAnAgentProblem(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	repository := f.Repository(t, "test")
	// Setup fails while the checkout has a marker file. The command is set
	// before any checkout is set up: the daemon's plugins cache a
	// repository's settings and miss later changes.
	revision := call(t, f.Clients.Repositories.GetRepositorySettings, &v1.GetRepositorySettingsRequest{RepositoryId: repository}).Revision
	call(t, f.Clients.Repositories.SetRepositorySetting, &v1.SetRepositorySettingRequest{RepositoryId: repository, Key: "plugins.setup.run", Value: &v1.SettingValue{Value: &v1.SettingValue_StringValue{StringValue: "test ! -e .fail-setup"}}, ExpectedRevision: revision})
	item, checkout := activeItem(t, f, repository, "failed start")
	id := item.Id
	observation := func() *v1.ItemObservation { return agentsItem(t, f, id).Observation }
	// The test driver refuses to launch this model.
	call(t, f.Clients.Agents.UpdateAgentSettings, &v1.UpdateAgentSettingsRequest{ItemId: id, Settings: map[string]string{"model": "test/fail-launch"}})
	started, err := f.Clients.Agents.StartAgent(t.Context(), connect.NewRequest(&v1.StartAgentRequest{ItemId: id, Mode: v1.AgentMode_AGENT_MODE_HEADLESS}))
	if err == nil && started.Msg.Job != nil {
		agentsWaitEnd(t, f, started.Msg.Job.Id)
	}
	reason := ""
	eventually(t, "the failed start as an agent problem", func() bool {
		o := observation()
		reason = o.AgentReason
		return o.AgentStatus == v1.AgentStatus_AGENT_STATUS_PROBLEM && o.NeedsAttention && reason != ""
	})
	// A failed setup is not a start.
	marker := filepath.Join(checkout.Claim.Path, ".fail-setup")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setup := call(t, f.Clients.Workspaces.SetupWorkspace, &v1.SetupWorkspaceRequest{ItemId: id})
	if job := agentsWaitEnd(t, f, setup.Job.Id); job.State != v1.JobState_JOB_STATE_FAILED {
		t.Fatalf("setup job: %v", job)
	}
	if o := observation(); o.AgentStatus != v1.AgentStatus_AGENT_STATUS_PROBLEM || o.AgentReason != reason {
		t.Fatalf("a failed setup hid the failed start: %v", o)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	apitest.Wait(t, f.Clients, call(t, f.Clients.Workspaces.SetupWorkspace, &v1.SetupWorkspaceRequest{ItemId: id}).Job)
	call(t, f.Clients.Agents.UpdateAgentSettings, &v1.UpdateAgentSettingsRequest{ItemId: id, Settings: map[string]string{"model": "test/model"}})
	startAgent(t, f, id)
	if o := observation(); o.AgentStatus == v1.AgentStatus_AGENT_STATUS_PROBLEM {
		t.Fatalf("problem after a successful start: %v", o)
	}
}

// agentsWaitEnd watches a job until it ends, however it ends.
func agentsWaitEnd(t testing.TB, f *apitest.Fixture, id string) *v1.Job {
	t.Helper()
	var job *v1.Job
	eventually(t, "job "+id+" to end", func() bool {
		job = call(t, f.Clients.Jobs.GetJob, &v1.GetJobRequest{JobId: id}).Job
		switch job.State {
		case v1.JobState_JOB_STATE_SUCCEEDED, v1.JobState_JOB_STATE_FAILED, v1.JobState_JOB_STATE_CANCELLED:
			return true
		}
		return false
	})
	return job
}

// Items get titles from the title model, or their first line when the model
// fails; SuggestTitle asks the model directly.
func TestAgentsTitlesComeFromTheTitleModel(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	repository := f.Repository(t, "test")
	profiles := call(t, f.Clients.Profiles.ListAgentProfiles, &v1.ListAgentProfilesRequest{})
	if profiles.TitleModel != nil {
		t.Fatalf("title model before one is set: %v", profiles.TitleModel)
	}
	refused(t, connect.CodeNotFound, f.Clients.Profiles.SetTitleModel, &v1.SetTitleModelRequest{Model: &v1.ModelChoice{Driver: "nonexistent"}, ExpectedRevision: profiles.Revision})
	set := call(t, f.Clients.Profiles.SetTitleModel, &v1.SetTitleModelRequest{Model: &v1.ModelChoice{Driver: "pi", Model: "test/model", Reasoning: "low"}, ExpectedRevision: profiles.Revision})
	refused(t, connect.CodeAborted, f.Clients.Profiles.SetTitleModel, &v1.SetTitleModelRequest{ExpectedRevision: profiles.Revision})
	if m := call(t, f.Clients.Profiles.ListAgentProfiles, &v1.ListAgentProfilesRequest{}).TitleModel; m == nil || m.Driver != "pi" || m.Model != "test/model" || m.Reasoning != "low" {
		t.Fatalf("title model: %v", m)
	}
	titled := func(description string) (string, []*v1.Job) {
		t.Helper()
		created := call(t, f.Clients.Items.CreateItem, &v1.CreateItemRequest{RepositoryId: repository, Description: description, GenerateTitle: true}).Item
		title := ""
		eventually(t, "a title for "+description, func() bool {
			title = agentsItem(t, f, created.Id).Item.Title
			return title != created.Title
		})
		return title, call(t, f.Clients.Jobs.ListJobs, &v1.ListJobsRequest{ItemId: created.Id}).Jobs
	}
	title, jobs := titled("write the docs")
	if title != "Test suggested title" {
		t.Fatalf("model title: %q", title)
	}
	// A model's title is its job's result too.
	if len(jobs) == 0 || jobs[0].Kind != "suggest_title" || jobs[0].GetTitle().GetTitle() != title {
		t.Fatalf("title job: %v", jobs)
	}
	// A fallback's job failed and has none.
	title, jobs = titled("Repair the importer\nthen fail the model")
	if title != "Repair the importer" {
		t.Fatalf("fallback title: %q", title)
	}
	for _, job := range jobs {
		if job.Kind == "suggest_title" && job.GetTitle() != nil {
			t.Fatalf("a failed title job has a result: %v", job)
		}
	}
	item := newItem(t, f, repository, "untitled").Id
	if suggested := call(t, f.Clients.Agents.SuggestTitle, &v1.SuggestTitleRequest{ItemId: item, Description: "write the docs"}).Title; suggested != "Test suggested title" {
		t.Fatalf("suggested title: %q", suggested)
	}
	call(t, f.Clients.Profiles.SetTitleModel, &v1.SetTitleModelRequest{ExpectedRevision: set.Revision})
	if model := call(t, f.Clients.Profiles.ListAgentProfiles, &v1.ListAgentProfilesRequest{}).TitleModel; model != nil {
		t.Fatalf("title model not cleared: %v", model)
	}
}

// A blocker's follow-up is sent once the item no longer waits, and an agent
// stopped on a usage limit gets a usage blocker whose follow-up continues it.
// The daemon checks blockers every few seconds: a sentinel item, created
// after the agent's and so checked after it in each pass, tells when a pass
// has run.
func TestAgentsFollowUpsAndUsageLimits(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.WithAgents())
	item, _ := startedAgent(t, f)
	sentinel := newItem(t, f, agentsItem(t, f, item).Item.Repository.Id, "sentinel").Id
	call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: sentinel, State: v1.ItemState_ITEM_STATE_ACTIVE})
	// Usage is known (here: no plugin reports it), so an expired usage
	// blocker clears on the next pass.
	call(t, f.Clients.System.GetUsage, &v1.GetUsageRequest{Wait: true})
	pass := func() {
		t.Helper()
		call(t, f.Clients.Items.AddBlocker, &v1.AddBlockerRequest{ItemId: sentinel, Kind: v1.BlockerKind_BLOCKER_KIND_USAGE, Until: timestamppb.New(time.Now().Add(-time.Minute))})
		eventually(t, "a blocker pass", func() bool { return len(agentsItem(t, f, sentinel).Item.Blockers) == 0 })
	}
	get := func() *v1.Item { return agentsItem(t, f, item).Item }
	usageBlocker := func() *v1.Blocker {
		for _, b := range get().Blockers {
			if b.Kind == v1.BlockerKind_BLOCKER_KIND_USAGE {
				return b
			}
		}
		return nil
	}

	t.Run("a follow-up waits for every blocker", func(t *testing.T) {
		first := call(t, f.Clients.Items.AddBlocker, &v1.AddBlockerRequest{ItemId: item, Kind: v1.BlockerKind_BLOCKER_KIND_EXTERNAL, Event: "review", FollowUp: "the review is in"}).Blocker
		second := call(t, f.Clients.Items.AddBlocker, &v1.AddBlockerRequest{ItemId: item, Kind: v1.BlockerKind_BLOCKER_KIND_EXTERNAL, Event: "ci"}).Blocker
		call(t, f.Clients.Items.ClearBlocker, &v1.ClearBlockerRequest{ItemId: item, BlockerId: first.Id})
		if followUps := get().FollowUps; len(followUps) != 1 || followUps[0].Message != "the review is in" {
			t.Fatalf("armed follow-ups: %v", followUps)
		}
		pass()
		if prompt := agentsLastPrompt(t, f, item); prompt == "the review is in" || len(get().FollowUps) != 1 {
			t.Fatalf("the follow-up was sent while the item still waited: %q", prompt)
		}
		call(t, f.Clients.Items.ClearBlocker, &v1.ClearBlockerRequest{ItemId: item, BlockerId: second.Id})
		eventually(t, "the follow-up", func() bool {
			return agentsLastPrompt(t, f, item) == "the review is in" && len(get().FollowUps) == 0
		})
		apitest.WaitIdle(t, f.Clients, item)
	})

	t.Run("a usage limit waits and continues", func(t *testing.T) {
		call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "this turn hits the usage limit"})
		var usage *v1.Blocker
		eventually(t, "a usage blocker", func() bool { usage = usageBlocker(); return usage != nil })
		// No usage plugin reports the test provider: it is retried in an hour.
		if usage.FollowUp != "Continue where you left off." || usage.Provider != "test" || usage.Until == nil || time.Until(usage.Until.AsTime()) < 50*time.Minute {
			t.Fatalf("usage blocker: %v", usage)
		}
		// The saved notice names whose quota ran out, as the blocker does.
		if last := agentsLast(t, f, item); !last.Error || last.UsageProvider != "test" {
			t.Fatalf("usage notice: %v", last)
		}
		// Clearing it continues the conversation; the next turn succeeds.
		call(t, f.Clients.Items.ClearBlocker, &v1.ClearBlockerRequest{ItemId: item, BlockerId: usage.Id})
		eventually(t, "the continue follow-up", func() bool { return agentsLastPrompt(t, f, item) == "Continue where you left off." })
		apitest.WaitIdle(t, f.Clients, item)
		pass()
		if blockers := get().Blockers; len(blockers) != 0 {
			t.Fatalf("the item waits again: %v", blockers)
		}
	})

	t.Run("a usage blocker cleared without its follow-up is not set again", func(t *testing.T) {
		call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: "another usage limit"})
		eventually(t, "a second usage blocker", func() bool { return usageBlocker() != nil })
		call(t, f.Clients.Items.ClearBlocker, &v1.ClearBlockerRequest{ItemId: item, ClearAll: true, DropFollowUp: true})
		pass()
		if value := get(); len(value.Blockers) != 0 || len(value.FollowUps) != 0 || agentsLastPrompt(t, f, item) != "another usage limit" {
			t.Fatalf("after dropping: %v, last prompt %q", value, agentsLastPrompt(t, f, item))
		}
	})
}
