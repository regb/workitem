package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
)

// Exercise Main's plugin host and its API-to-settings JSON conversion. Calling
// github.Plugin directly misses the default empty-list settings on this path.
func TestGitHubWaitBinaryWithDefaultEmptyPluginLists(t *testing.T) {
	f := newBinaryFixture(t)
	plugin := apitest.Command(t, "./cmd/wi-github")
	state := filepath.Join(f.root, "gh-state.json")
	writeState := func(value string) {
		t.Helper()
		if err := os.WriteFile(state, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeState(`{"state":"OPEN"}`)
	fake := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  api) printf 'waiting-user\\n';;\n  pr) exec /bin/cat %q;;\n  *) exit 1;;\nesac\n", state)
	if err := os.WriteFile(filepath.Join(f.root, "gh"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	for i, entry := range f.env {
		if strings.HasPrefix(entry, "PATH=") {
			f.env[i] = "PATH=" + f.root + string(os.PathListSeparator) + strings.TrimPrefix(entry, "PATH=")
		}
	}
	config := filepath.Join(f.root, "config", "wi", "config.toml")
	initial, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, append(initial, []byte(fmt.Sprintf("\n[plugins.github]\nenabled = true\ncommand = [%q]\n", plugin))...), 0600); err != nil {
		t.Fatal(err)
	}
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "github-test", "--from", f.source, "--branch", "main")
	var created v1.CreateItemResponse
	f.json(&created, "new", "--repo", "github-test", "--title", "Wait for review")
	item := created.Item.Id
	// Review and plugin commands share the ordinary operator endpoint.
	f.run("review", "status", "--item", item)
	url := "https://github.com/NetFabric-AI/netfabric-code/pull/3556"
	f.run("github", "wait", url, "--item", item)
	var shown v1.GetItemResponse
	f.json(&shown, "show", item)
	if len(shown.Item.Blockers) != 1 || shown.Item.Blockers[0].Meta["github.pr"] != "NetFabric-AI/netfabric-code#3556" {
		t.Fatal(shown.String())
	}
	// A caller whose credential is not valid gains nothing by choosing a
	// plugin or review command: the daemon refuses every call.
	for _, args := range [][]string{{"wait", "--clear", "--item", item}, {"review", "status", "--item", item}} {
		sandbox := exec.Command(f.wi, args...)
		sandbox.Env = append(append([]string{}, f.env...), "WI_ITEM_CREDENTIAL_FILE="+filepath.Join(f.root, "missing-credential"))
		sandbox.Dir = f.root
		if out, err := sandbox.CombinedOutput(); err == nil || !strings.Contains(string(out), "credential") {
			t.Fatalf("%v without a credential: %v %s", args, err, out)
		}
	}
	f.json(&shown, "show", item)
	if len(shown.Item.Blockers) != 1 {
		t.Fatal("a refused call changed the wait", shown.String())
	}
	writeState(fmt.Sprintf(`{"state":"OPEN","reviews":[{"author":{"login":"reviewer"},"state":"APPROVED","submittedAt":%q}]}`, time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)))
	f.run("plugin", "run", "github")
	f.json(&shown, "show", item)
	if len(shown.Item.Blockers) != 0 {
		t.Fatal("scheduled plugin path did not clear reviewed wait", shown.String())
	}
}
