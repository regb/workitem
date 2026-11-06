package cli

import (
	"context"
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestDocumentedBlockerFlagsSelectAndClearTheRightItem(t *testing.T) {
	f := newClientFixture(t)
	current := f.create(t, "Current")
	target := f.create(t, "Target")
	f.cfg.Env["WI_ID"] = current.Id
	f.run(t, "blocker", "add", "--kind", "external", "--event", "PR activity", "--meta", "github.pr=acme/api#42", target.Id)
	var shown v1.GetItemResponse
	f.decode(t, f.run(t, "show", target.Id, "--json"), &shown)
	if len(shown.Item.Blockers) != 1 || shown.Item.Blockers[0].Kind != v1.BlockerKind_BLOCKER_KIND_EXTERNAL {
		t.Fatal(shown.String())
	}
	blockerID := shown.Item.Blockers[0].Id
	for _, args := range [][]string{
		{"blocker", "add", "--kind", "unknown", "--event", "bad"},
		{"blocker", "add", "--kind", "time", "--event", "bad"},
		{"blocker", "add", "--kind", "external", "--on", current.Id},
		{"blocker", "add", "--until", "2h", "--event", "bad"},
		{"blocker", "clear", "--id", blockerID, "--all"},
		{"blocker", "clear", "--all", "--event", "bad"},
		{"blocker", "unknown"},
	} {
		f.out.Reset()
		f.errs.Reset()
		if code := Run(context.Background(), append(args, "--item", target.Id), f.cfg); code != ExitUsage {
			t.Fatalf("%v: code=%d stderr=%s", args, code, f.errs)
		}
	}
	f.decode(t, f.run(t, "show", target.Id, "--json"), &shown)
	if len(shown.Item.Blockers) != 1 || shown.Item.Blockers[0].Id != blockerID {
		t.Fatal("invalid flags changed blockers", shown.String())
	}
	f.run(t, "blocker", "add", "--kind", "time", "--until", "2h", "--item", target.Id)
	f.run(t, "blocker", "add", "--kind", "item", "--on", current.Id, "--item", target.Id)
	f.run(t, "blocker", "clear", "--id", blockerID, target.Id)
	f.decode(t, f.run(t, "show", target.Id, "--json"), &shown)
	if len(shown.Item.Blockers) != 2 {
		t.Fatal("--id did not clear exactly one blocker", shown.String())
	}
	f.run(t, "blocker", "clear", "--all", target.Id)
	f.decode(t, f.run(t, "show", target.Id, "--json"), &shown)
	if len(shown.Item.Blockers) != 0 {
		t.Fatal(shown.String())
	}
	f.decode(t, f.run(t, "show", current.Id, "--json"), &shown)
	if len(shown.Item.Blockers) != 0 {
		t.Fatal("positional item selector mutated the current item", shown.String())
	}
}
