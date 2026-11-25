package daemonapi

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
)

// A message from another item's agent is enveloped with how the items
// relate; a person's, or an agent's to its own item, is not.
func TestAgentMessagesAreEnveloped(t *testing.T) {
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
	for _, sender := range []string{model.OperatorUser, model.ItemPrincipal(c)} {
		if e, err := f.api.messageEnvelope(ctx, c, sender); err != nil || e.Marker != "" {
			t.Fatalf("%s: %v %v", sender, e, err)
		}
	}
	down, err := f.api.messageEnvelope(ctx, c, model.ItemPrincipal(p))
	if err != nil || down.Relation != model.RelationOwner || down.Title != "parent" || down.Marker == "" {
		t.Fatalf("to the delegation: %+v %v", down, err)
	}
	up, err := f.api.messageEnvelope(ctx, p, model.ItemPrincipal(c))
	if err != nil || up.Relation != model.RelationDelegation {
		t.Fatalf("to the owner: %+v %v", up, err)
	}
}

// A blocker records who set it, so its follow-up is sent as them: an
// agent's reaches the item enveloped, never as the operator's.
func TestBlockersRecordWhoSetThem(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo := f.repo(t)
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "waits"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	other, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "setter"}))
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := f.agentClients(t, other.Msg.Item.Id)
	if _, err := f.clients.Items.GrantAccess(ctx, connect.NewRequest(&v1.GrantAccessRequest{Subject: model.ItemPrincipal(other.Msg.Item.Id), OnItemId: id, Level: v1.AccessLevel_ACCESS_LEVEL_MESSAGE})); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Items.AddBlocker(ctx, connect.NewRequest(&v1.AddBlockerRequest{ItemId: id, Kind: v1.BlockerKind_BLOCKER_KIND_EXTERNAL, Event: "review", FollowUp: "continue"})); err != nil {
		t.Fatal(err)
	}
	m, err := f.api.App.Store.LoadManifest(id)
	if err != nil || len(m.Blockers) != 1 || m.Blockers[0].SetBy != model.ItemPrincipal(other.Msg.Item.Id) {
		t.Fatalf("blockers: %+v %v", m.Blockers, err)
	}
}
