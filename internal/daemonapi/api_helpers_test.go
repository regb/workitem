package daemonapi_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/apitest/runwi"
)

// Shared helpers of the black-box suite (the api_*_test.go files). They use
// only the fixture and the generated clients.

func TestMain(m *testing.M) { os.Exit(runwi.Main(m)) }

// rpc is a unary procedure of a generated client.
type rpc[Req, Res any] func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error)

// call calls a unary procedure and fails the test unless it succeeds.
func call[Req, Res any](t testing.TB, procedure rpc[Req, Res], request *Req) *Res {
	t.Helper()
	response, err := procedure(t.Context(), connect.NewRequest(request))
	if err != nil {
		t.Fatalf("%T: %v", request, err)
	}
	return response.Msg
}

// refused calls a unary procedure and fails the test unless it fails with
// code. It returns the error, for its details.
func refused[Req, Res any](t testing.TB, code connect.Code, procedure rpc[Req, Res], request *Req) error {
	t.Helper()
	response, err := procedure(t.Context(), connect.NewRequest(request))
	if connect.CodeOf(err) != code {
		t.Fatalf("%T: want %v, got %v %v", request, code, response, err)
	}
	return err
}

// eventually polls until done holds, and fails the test with what if it does
// not within a few seconds.
func eventually(t testing.TB, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// newItem creates a backlog item in repository.
func newItem(t testing.TB, f *apitest.Fixture, repository, title string) *v1.Item {
	t.Helper()
	return call(t, f.Clients.Items.CreateItem, &v1.CreateItemRequest{RepositoryId: repository, Title: title}).Item
}

// activeItem creates an active item in repository with its checkout, and
// returns the item and the checkout.
func activeItem(t testing.TB, f *apitest.Fixture, repository, title string) (*v1.Item, *v1.Checkout) {
	t.Helper()
	item := newItem(t, f, repository, title)
	call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: item.Id, State: v1.ItemState_ITEM_STATE_ACTIVE})
	ensured := call(t, f.Clients.Workspaces.EnsureWorkspace, &v1.EnsureWorkspaceRequest{ItemId: item.Id})
	apitest.Wait(t, f.Clients, ensured.Job)
	got := call(t, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: item.Id})
	return got.Item, got.Item.Checkout
}

// startAgent starts item's agent headless, and returns it once idle.
func startAgent(t testing.TB, f *apitest.Fixture, item string) *v1.Agent {
	t.Helper()
	started := call(t, f.Clients.Agents.StartAgent, &v1.StartAgentRequest{ItemId: item, Mode: v1.AgentMode_AGENT_MODE_HEADLESS})
	if started.Job != nil {
		apitest.Wait(t, f.Clients, started.Job)
	}
	return apitest.WaitIdle(t, f.Clients, item)
}

// startedAgent starts a headless agent, the fake driver's, on a new active
// item with a checkout in a new repository, and returns the item and its idle
// agent. The fixture must run agents.
func startedAgent(t testing.TB, f *apitest.Fixture) (string, *v1.Agent) {
	t.Helper()
	item, _ := activeItem(t, f, f.Repository(t, "test"), "agent")
	return item.Id, startAgent(t, f, item.Id)
}

// send prompts item's agent and waits until it has answered and is idle.
func send(t testing.TB, f *apitest.Fixture, item, text string) {
	t.Helper()
	before := len(call(t, f.Clients.Agents.GetHistory, &v1.GetHistoryRequest{ItemId: item}).Messages)
	call(t, f.Clients.Agents.SendMessage, &v1.SendMessageRequest{ItemId: item, Text: text, Delivery: v1.Delivery_DELIVERY_PROMPT})
	apitest.WaitHistory(t, f.Clients, item, before+2)
	apitest.WaitIdle(t, f.Clients, item)
}

// messageText is a message's text parts, joined as a reader sees them.
func messageText(message *v1.ConversationMessage) string {
	var parts []string
	for _, part := range message.GetParts() {
		if text, ok := part.Kind.(*v1.Part_Text); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// git runs git in dir as a test author and returns its trimmed output.
func git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-c", "user.name=API test", "-c", "user.email=test@example.invalid", "-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
