package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// Exercise Main's plugin host and its API-to-settings JSON conversion. Calling
// github.Plugin directly misses the default empty-list settings on this path.
func TestGitHubWaitBinaryWithDefaultEmptyPluginLists(t *testing.T) {
	f := newBinaryFixture(t)
	_, source, _, _ := runtime.Caller(0)
	plugin := filepath.Join(f.root, "wi-github")
	build := exec.Command("go", "build", "-o", plugin, "./cmd/wi-github")
	build.Dir = filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build GitHub plugin: %v %s", err, out)
	}
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
	url := "https://github.com/NetFabric-AI/netfabric-code/pull/3556"
	f.run("github", "wait", url, "--item", item)
	var shown v1.GetItemResponse
	f.json(&shown, "show", item)
	if len(shown.Item.Blockers) != 1 || shown.Item.Blockers[0].Meta["github.pr"] != "NetFabric-AI/netfabric-code#3556" {
		t.Fatal(shown.String())
	}
	writeState(fmt.Sprintf(`{"state":"OPEN","reviews":[{"author":{"login":"reviewer"},"state":"APPROVED","submittedAt":%q}]}`, time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)))
	f.run("plugin", "run", "github")
	f.json(&shown, "show", item)
	if len(shown.Item.Blockers) != 0 {
		t.Fatal("scheduled plugin path did not clear reviewed wait", shown.String())
	}
}
