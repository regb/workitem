package daemonapi

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/credential"
	"github.com/regb/workitem/internal/model"
)

// Who a call is from is the credential it presents, never a header: no
// credential, a forged one, or another kind's are refused, and caller
// metadata changes nothing.
func TestCallersAreWhoTheirCredentialSays(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	info := func(t *testing.T, clients interface {
		GetInfo(context.Context, *connect.Request[v1.GetInfoRequest]) (*connect.Response[v1.GetInfoResponse], error)
	}) (*v1.Caller, error) {
		t.Helper()
		req := connect.NewRequest(&v1.GetInfoRequest{})
		req.Header().Set(HolderHeader, "agent")
		req.Header().Set("Wi-Endpoint-Access", "operator")
		value, err := clients.GetInfo(ctx, req)
		if err != nil {
			return nil, err
		}
		return value.Msg.Caller, nil
	}
	operator, err := info(t, f.clients.System)
	if err != nil || operator.Kind != v1.CallerKind_CALLER_KIND_LOCAL_OPERATOR || operator.Principal != OperatorUser || operator.ItemId != "" {
		t.Fatalf("operator: %v %v", operator, err)
	}
	web, err := info(t, f.webClients(t).System)
	if err != nil || web.Kind != v1.CallerKind_CALLER_KIND_WEB_ADAPTER || web.Principal != OperatorUser {
		t.Fatalf("web: %v %v", web, err)
	}
	forged, _ := credential.New(credential.Operator, "")
	for name, token := range map[string]string{"none": "", "garbage": "not a credential", "forged operator": forged.Token()} {
		token := token
		clients := f.clientsWith(t, func() (string, error) { return token, nil })
		if _, err := info(t, clients.System); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("%s: %v", name, err)
		}
	}
	repo := f.repo(t)
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "agent identity"}))
	if err != nil {
		t.Fatal(err)
	}
	itemID := created.Msg.Item.Id
	agent, runtime := f.agentClients(t, itemID)
	caller, err := info(t, agent.System)
	if err != nil || caller.Kind != v1.CallerKind_CALLER_KIND_AGENT || caller.Principal != "item:"+itemID || caller.ItemId != itemID {
		t.Fatalf("agent: %v %v", caller, err)
	}
	// Streams check the credential too.
	stream, err := agent.Events.WatchChanges(ctx, connect.NewRequest(&v1.WatchChangesRequest{}))
	if err != nil || !stream.Receive() {
		t.Fatalf("agent stream: %v %v", err, stream.Err())
	}
	stream.Close()
	// A new runtime taking the item over ends the old one's credential.
	newer, _ := f.agentClients(t, itemID)
	if _, err := info(t, agent.System); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("replaced runtime: %v", err)
	}
	if _, err := info(t, newer.System); err != nil {
		t.Fatalf("current runtime: %v", err)
	}
	// So does the runtime stopping.
	stopped := time.Now().UTC()
	current, err := f.api.App.Store.LoadAgentRuntime(itemID)
	if err != nil || current == nil || current.ID == runtime.ID {
		t.Fatalf("runtime: %v %v", current, err)
	}
	current.State, current.StoppedAt = model.AgentRuntimeStopped, &stopped
	if err := f.api.App.Store.SaveAgentRuntime(ctx, itemID, *current); err != nil {
		t.Fatal(err)
	}
	if _, err := info(t, newer.System); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("stopped runtime: %v", err)
	}
	stream, err = newer.Events.WatchChanges(ctx, connect.NewRequest(&v1.WatchChangesRequest{}))
	if err == nil {
		stream.Receive()
		err = stream.Err()
		stream.Close()
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("stopped runtime stream: %v", err)
	}
}

// What a caller does is recorded as theirs: an item's agent acts as the
// item, for the operator; the operator as themselves.
func TestActionsRecordWhoDidThem(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo := f.repo(t)
	parent, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "parent"}))
	if err != nil {
		t.Fatal(err)
	}
	parentID := parent.Msg.Item.Id
	agent, _ := f.agentClients(t, parentID)
	child, err := agent.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "child"}))
	if err != nil {
		t.Fatal(err)
	}
	childID := child.Msg.Item.Id
	if _, err := agent.Items.UpdateItem(ctx, connect.NewRequest(&v1.UpdateItemRequest{ItemId: childID, AddLabels: []string{"triage"}})); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Items.MarkRead(ctx, connect.NewRequest(&v1.MarkReadRequest{ItemId: childID})); err != nil {
		t.Fatal(err)
	}
	events := func(itemID string) []*v1.ItemEvent {
		t.Helper()
		value, err := f.clients.Items.ListItemEvents(ctx, connect.NewRequest(&v1.ListItemEventsRequest{ItemId: itemID}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Events
	}
	agentPrincipal := model.ItemPrincipal(parentID)
	seen := map[string]bool{}
	for _, event := range events(childID) {
		seen[event.Type] = true
		if event.Actor != agentPrincipal || event.ForUser != model.OperatorUser {
			t.Fatalf("%s recorded as %q for %q", event.Type, event.Actor, event.ForUser)
		}
	}
	for _, kind := range []string{"work_item.created", "attention.read"} {
		if !seen[kind] {
			t.Fatalf("no %s event: %v", kind, seen)
		}
	}
	for _, event := range events(parentID) {
		if event.Type == "work_item.created" && (event.Actor != model.OperatorUser || event.ForUser != model.OperatorUser) {
			t.Fatalf("operator's creation recorded as %q for %q", event.Actor, event.ForUser)
		}
	}
}
