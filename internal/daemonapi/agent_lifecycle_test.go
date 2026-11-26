package daemonapi_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"google.golang.org/protobuf/encoding/protojson"
)

// startedAgent starts a headless agent, the fake driver's, on a new active
// item with a checkout, and returns the item and its idle agent.
func startedAgent(t *testing.T, f *apitest.Fixture) (string, *v1.Agent) {
	t.Helper()
	ctx := context.Background()
	repository := f.Repository(t, "test")
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repository, Title: "agent"}))
	if err != nil {
		t.Fatal(err)
	}
	item := created.Msg.Item.Id
	if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: item, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	workspace, err := f.Clients.Workspaces.EnsureWorkspace(ctx, connect.NewRequest(&v1.EnsureWorkspaceRequest{ItemId: item}))
	if err != nil {
		t.Fatal(err)
	}
	apitest.Wait(t, f.Clients, workspace.Msg.Job)
	started, err := f.Clients.Agents.StartAgent(ctx, connect.NewRequest(&v1.StartAgentRequest{ItemId: item, Mode: v1.AgentMode_AGENT_MODE_HEADLESS}))
	if err != nil {
		t.Fatal(err)
	}
	if started.Msg.Job != nil {
		apitest.Wait(t, f.Clients, started.Msg.Job)
	}
	return item, apitest.WaitIdle(t, f.Clients, item)
}

func TestAnAgentStartsAnswersAndStops(t *testing.T) {
	f := apitest.New(t, apitest.WithAgents())
	ctx := context.Background()
	item, agent := startedAgent(t, f)
	if agent.Mode != v1.AgentMode_AGENT_MODE_HEADLESS || agent.Generation == 0 {
		t.Fatalf("started agent: %v", agent)
	}
	if _, err := f.Clients.Agents.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: item, Text: "hello", Delivery: v1.Delivery_DELIVERY_PROMPT, ExpectedGeneration: agent.Generation})); err != nil {
		t.Fatal(err)
	}
	messages := apitest.WaitHistory(t, f.Clients, item, 2)
	apitest.WaitIdle(t, f.Clients, item)
	last, err := protojson.Marshal(messages[len(messages)-1])
	if err != nil {
		t.Fatal(err)
	}
	if messages[len(messages)-1].Role != v1.MessageRole_MESSAGE_ROLE_ASSISTANT || !strings.Contains(string(last), "Echo: hello") {
		t.Fatalf("the agent's answer: %s", last)
	}
	if _, err := f.Clients.Agents.StopAgent(ctx, connect.NewRequest(&v1.StopAgentRequest{ItemId: item})); err != nil {
		t.Fatal(err)
	}
	stopped, err := f.Clients.Agents.GetAgent(ctx, connect.NewRequest(&v1.GetAgentRequest{ItemId: item}))
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Msg.Agent.Online {
		t.Fatalf("stopped agent is online: %v", stopped.Msg.Agent)
	}
}

func TestRestartingAnotherGenerationIsRefused(t *testing.T) {
	f := apitest.New(t, apitest.WithAgents())
	item, agent := startedAgent(t, f)
	_, err := f.Clients.Agents.RestartAgent(context.Background(), connect.NewRequest(&v1.RestartAgentRequest{ItemId: item, ExpectedGeneration: agent.Generation + 1}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale generation: %v", err)
	}
	if current := apitest.WaitIdle(t, f.Clients, item); current.Generation != agent.Generation {
		t.Fatalf("a refused restart replaced the agent: %d -> %d", agent.Generation, current.Generation)
	}
}
