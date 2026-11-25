package daemonapi

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

// A message from another item's agent is enveloped with how the items
// relate and how many agent messages led to it; a chain without a person
// stops.
func TestAgentMessagesAreEnvelopedAndBounded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo := f.repo(t)
	parent, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "parent"}))
	if err != nil {
		t.Fatal(err)
	}
	p := parent.Msg.Item.Id
	agent, _ := f.agentClients(t, p)
	child, err := agent.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "child"}))
	if err != nil {
		t.Fatal(err)
	}
	c := child.Msg.Item.Id
	// A person's message, or an agent's to its own item, is not enveloped.
	for _, sender := range []string{model.OperatorUser, model.ItemPrincipal(c)} {
		if e, err := f.api.messageEnvelope(ctx, c, sender); err != nil || e.Marker != "" {
			t.Fatalf("%s: %v %v", sender, e, err)
		}
	}
	down, err := f.api.messageEnvelope(ctx, c, model.ItemPrincipal(p))
	if err != nil || down.Relation != model.RelationOwner || down.Hops != 1 || down.Title != "parent" || down.Marker == "" {
		t.Fatalf("to the delegation: %+v %v", down, err)
	}
	up, err := f.api.messageEnvelope(ctx, p, model.ItemPrincipal(c))
	if err != nil || up.Relation != model.RelationDelegation {
		t.Fatalf("to the owner: %+v %v", up, err)
	}
	// The parent's turn came from a chain of agent messages at the limit.
	if err := f.core.Database().RecordAPIMessage(coordinator.APIMessageReceipt{ItemID: p, MessageID: "m1", RuntimeID: "r", Generation: 1, CreatedAt: time.Now().UTC(), Sender: model.ItemPrincipal(c), Hops: model.MaxAgentHops}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.api.messageEnvelope(ctx, c, model.ItemPrincipal(p)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("past the limit: %v", err)
	}
	// A person's message resets it.
	if err := f.core.Database().RecordAPIMessage(coordinator.APIMessageReceipt{ItemID: p, MessageID: "m2", RuntimeID: "r", Generation: 1, CreatedAt: time.Now().UTC().Add(time.Second), Sender: model.OperatorUser}); err != nil {
		t.Fatal(err)
	}
	if e, err := f.api.messageEnvelope(ctx, c, model.ItemPrincipal(p)); err != nil || e.Hops != 1 {
		t.Fatalf("after a person's message: %+v %v", e, err)
	}
}
