package cli

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestContextualBasesResolveInClientCheckout(t *testing.T) {
	f := newClientFixture(t)
	var created v1.CreateItemResponse
	f.decode(t, f.run(t, "new", "--repo", f.repo, "--title", "base context", "--json"), &created)
	f.run(t, "state", "set", "active", created.Item.Id, "--json")
	var workspace v1.EnsureWorkspaceResponse
	f.decode(t, f.run(t, "workspace", "ensure", created.Item.Id, "--skip-setup", "--json"), &workspace)
	root := workspace.Checkout.Claim.Path
	f.cfg.CWD = root
	f.cfg.Env["WI_ID"] = created.Item.Id
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("config", "user.name", "Base test")
	git("config", "user.email", "test@example.invalid")
	branch := git("branch", "--show-current")
	initial := git("rev-parse", "HEAD")
	git("switch", "-c", "context-previous")
	git("commit", "--allow-empty", "-m", "previous checkout")
	previous := git("rev-parse", "HEAD")
	git("switch", branch)
	for _, test := range []struct{ base, commit string }{{"-", previous}, {"@{-1}", previous}, {"HEAD", initial}, {"HEAD^{commit}", initial}, {"@", initial}, {"context-previous~1", initial}} {
		t.Run(test.base, func(t *testing.T) {
			var item v1.CreateItemResponse
			f.decode(t, f.run(t, "new", "--repo", f.repo, "--title", "resolved base", "--base="+test.base, "--json"), &item)
			if item.Item.CreatedFromCommit != test.commit {
				t.Fatalf("%s: %s != %s", test.base, item.Item.CreatedFromCommit, test.commit)
			}
		})
	}
	f.cfg.CWD = f.api.Root
	if _, err := rpcBase(context.Background(), f.cfg, f.repo, "HEAD"); err == nil {
		t.Fatal("context escaped the claimed checkout")
	}
	f.cfg.CWD = root
	if _, err := rpcBase(context.Background(), f.cfg, "another-repository", "@"); err == nil {
		t.Fatal("context crossed repositories")
	}
	if result, err := rpcBase(context.Background(), Config{}, "unused", "v1.0"); err != nil || result != "v1.0" {
		t.Fatalf("named base should be forwarded: %s %v", result, err)
	}
}
