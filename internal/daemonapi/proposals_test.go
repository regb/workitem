package daemonapi

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
)

// An agent proposes work for its user; the user files it as their own, with
// their edits, or dismisses it. Nothing exists until it is filed.
func TestAgentsProposeAndUsersFile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo := f.repo(t)
	create := func(title string) string {
		value, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: title}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Item.Id
	}
	source, other := create("source"), create("other")
	agent, _ := f.agentClients(t, source)
	propose := func(itemID, request string) (*v1.Proposal, error) {
		value, err := agent.Items.ProposeItem(ctx, connect.NewRequest(&v1.ProposeItemRequest{ItemId: itemID, Title: "Flaky reopen test", Description: "It failed once on reopen.", Labels: []string{"bug"}, RequestId: request}))
		if err != nil {
			return nil, err
		}
		return value.Msg.Proposal, nil
	}
	if _, err := propose(other, "x"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("proposed from another item: %v", err)
	}
	first, err := propose(source, "one")
	if err != nil {
		t.Fatal(err)
	}
	again, err := propose(source, "one")
	if err != nil || again.Id != first.Id {
		t.Fatalf("a retried proposal: %v %v", again, err)
	}
	second, err := propose(source, "two")
	if err != nil {
		t.Fatal(err)
	}
	if first.Proposer != model.ItemPrincipal(source) || first.State != v1.ProposalState_PROPOSAL_STATE_PENDING || first.RepositoryId != repo {
		t.Fatalf("proposal: %v", first)
	}
	listed, err := f.clients.Items.ListProposals(ctx, connect.NewRequest(&v1.ListProposalsRequest{PendingOnly: true}))
	if err != nil || len(listed.Msg.Proposals) != 2 {
		t.Fatalf("pending: %v %v", listed, err)
	}
	items, _ := f.clients.Items.ListItems(ctx, connect.NewRequest(&v1.ListItemsRequest{}))
	if len(items.Msg.Items) != 2 {
		t.Fatalf("a proposal created an item: %v", items.Msg.Items)
	}
	// Only a person decides.
	if _, err := agent.Items.FileProposal(ctx, connect.NewRequest(&v1.FileProposalRequest{ProposalId: first.Id})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an agent filed: %v", err)
	}
	if _, err := agent.Items.DismissProposal(ctx, connect.NewRequest(&v1.DismissProposalRequest{ProposalId: first.Id})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an agent dismissed: %v", err)
	}
	filed, err := f.clients.Items.FileProposal(ctx, connect.NewRequest(&v1.FileProposalRequest{ProposalId: first.Id, Title: "Retry reopen on a revision bump"}))
	if err != nil {
		t.Fatal(err)
	}
	created := filed.Msg.Item
	if created.Title != "Retry reopen on a revision bump" || created.Owner != model.OperatorUser || len(created.Labels) != 1 || filed.Msg.Proposal.State != v1.ProposalState_PROPOSAL_STATE_FILED || filed.Msg.Proposal.FiledItemId != created.Id {
		t.Fatalf("filed: %v", filed.Msg)
	}
	// Filing again is the same item.
	if again, err := f.clients.Items.FileProposal(ctx, connect.NewRequest(&v1.FileProposalRequest{ProposalId: first.Id})); err != nil || again.Msg.Item.Id != created.Id {
		t.Fatalf("filed again: %v %v", again, err)
	}
	events, err := f.clients.Items.ListItemEvents(ctx, connect.NewRequest(&v1.ListItemEventsRequest{ItemId: created.Id}))
	if err != nil || events.Msg.Events[0].Actor != model.OperatorUser || events.Msg.Events[0].Cause != "proposal:"+first.Id {
		t.Fatalf("creation event: %v %v", events, err)
	}
	if _, err := f.clients.Items.DismissProposal(ctx, connect.NewRequest(&v1.DismissProposalRequest{ProposalId: first.Id})); err == nil {
		t.Fatal("dismissed a filed proposal")
	}
	dismissed, err := f.clients.Items.DismissProposal(ctx, connect.NewRequest(&v1.DismissProposalRequest{ProposalId: second.Id, Reason: "duplicate"}))
	if err != nil || dismissed.Msg.Proposal.State != v1.ProposalState_PROPOSAL_STATE_DISMISSED || dismissed.Msg.Proposal.DecidedBy != model.OperatorUser {
		t.Fatalf("dismiss: %v %v", dismissed, err)
	}
	if _, err := f.clients.Items.FileProposal(ctx, connect.NewRequest(&v1.FileProposalRequest{ProposalId: second.Id})); err == nil {
		t.Fatal("filed a dismissed proposal")
	}
	view, err := f.clients.Views.GetItemView(ctx, connect.NewRequest(&v1.GetItemViewRequest{ItemId: source}))
	if err != nil || len(view.Msg.Proposals) != 2 {
		t.Fatalf("view: %v %v", view, err)
	}
}
