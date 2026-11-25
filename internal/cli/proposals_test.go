package cli

import (
	"strings"
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// An agent asked to file something proposes it; its user files or
// dismisses it. An agent's plain wi new delegates instead.
func TestProposeFileAndDelegateFromTheCommandLine(t *testing.T) {
	f := newClientFixture(t)
	source := f.create(t, "Source")
	// An active owner follows its delegations up, so they stay out of the
	// user's list.
	f.run(t, "state", "set", "active", source.Id)
	operator := f.cfg.API
	f.cfg.API = f.api.AgentClients(t, source.Id)
	f.cfg.Env["WI_ID"] = source.Id
	var proposed v1.ProposeItemResponse
	f.decode(t, f.run(t, "new", "--propose", "--title", "Retry reopen", "--label", "bug", "--json"), &proposed)
	if proposed.Proposal.ItemId != source.Id || proposed.Proposal.State != v1.ProposalState_PROPOSAL_STATE_PENDING {
		t.Fatal(proposed.String())
	}
	if !strings.Contains(f.run(t, "new", "--propose", "--title", "Second thought"), "waiting for the user") {
		t.Fatal(f.out.String())
	}
	var delegated v1.CreateItemResponse
	f.decode(t, f.run(t, "new", "--title", "Split work", "--json"), &delegated)
	if delegated.Item.Owner != "item:"+source.Id {
		t.Fatalf("an agent's new item: %s", delegated.Item.Owner)
	}
	f.cfg.API = operator
	listed := f.run(t, "proposal", "list")
	if !strings.Contains(listed, "Retry reopen") || !strings.Contains(listed, "Second thought") {
		t.Fatal(listed)
	}
	var filed v1.FileProposalResponse
	f.decode(t, f.run(t, "proposal", "file", proposed.Proposal.Id, "--title", "Retry reopen on a revision bump", "--json"), &filed)
	if filed.Item.Title != "Retry reopen on a revision bump" || filed.Item.Owner != "user:operator" {
		t.Fatal(filed.String())
	}
	if strings.Contains(f.run(t, "proposal", "list"), "Retry reopen") {
		t.Fatal("a filed proposal is still pending")
	}
	// The delegated item is not in the user's list until they take it.
	if strings.Contains(f.run(t, "list", "--no-agent"), "split-work") {
		t.Fatal("a delegated item is in the user's list")
	}
	if !strings.Contains(f.run(t, "list", "--no-agent", "--delegated-by", source.Id), "split-work") {
		t.Fatalf("the item's delegations: %s", f.out)
	}
	f.run(t, "own", "--item", delegated.Item.Id)
	if !strings.Contains(f.run(t, "list", "--no-agent"), "split-work") {
		t.Fatal("a taken item is not in the user's list")
	}
	if code := Run(t.Context(), []string{"new", "--propose", "--title", "x", "--start"}, f.cfg); code != ExitUsage {
		t.Fatalf("--propose with --start: %d", code)
	}
}
