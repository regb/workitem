package cli

import (
	"strings"
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// An agent's new item is its item's; the user takes it, or hands it to
// another item, with wi edit --owner. An agent cannot change owners.
func TestEditOwnerFromTheCommandLine(t *testing.T) {
	f := newClientFixture(t)
	parent, other := f.create(t, "Parent"), f.create(t, "Other")
	f.run(t, "state", "set", "active", parent.Id)
	operator := f.cfg.API
	f.cfg.API = f.api.AgentClients(t, parent.Id)
	f.cfg.Env["WI_ID"] = parent.Id
	var delegated v1.CreateItemResponse
	f.decode(t, f.run(t, "new", "--title", "Split work", "--json"), &delegated)
	if delegated.Item.Owner != "item:"+parent.Id {
		t.Fatalf("an agent's new item: %s", delegated.Item.Owner)
	}
	if code := Run(t.Context(), []string{"edit", "--item", delegated.Item.Id, "--owner", "me"}, f.cfg); code == ExitOK {
		t.Fatal("an agent changed an owner")
	}
	f.cfg.API = operator
	delete(f.cfg.Env, "WI_ID")
	if strings.Contains(f.run(t, "list", "--no-agent"), "split-work") {
		t.Fatal("a delegated item is in the user's list")
	}
	f.run(t, "edit", "--item", delegated.Item.Id, "--owner", other.Id)
	var shown v1.GetItemResponse
	f.decode(t, f.run(t, "show", delegated.Item.Id, "--json"), &shown)
	if shown.Item.Owner != "item:"+other.Id {
		t.Fatalf("handed to: %s", shown.Item.Owner)
	}
	f.run(t, "edit", "--item", delegated.Item.Id, "--owner", "me")
	if !strings.Contains(f.run(t, "list", "--no-agent"), "split-work") {
		t.Fatal("a taken item is not in the user's list")
	}
}

func TestAgentsStartHeadlessByDefault(t *testing.T) {
	if got := defaultAgentMode(Config{Env: map[string]string{}}); got != "tui" {
		t.Fatalf("person default = %q", got)
	}
	if got := defaultAgentMode(Config{Env: map[string]string{"WI_ITEM_CREDENTIAL_FILE": "/state/credential"}}); got != "headless" {
		t.Fatalf("agent default = %q", got)
	}
}
