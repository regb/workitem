package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func (f *binaryFixture) cliControlCompatibility(item string) {
	f.t.Helper()
	file := filepath.Join(f.root, "cli-prompt.txt")
	if err := os.WriteFile(file, []byte("legacy file prompt"), 0600); err != nil {
		f.t.Fatal(err)
	}
	f.run("agent", "control", "send", "--item", item, "--actor", "smoke", "--file", file)
	f.historyReady(item, 6)
	f.idle(item)
	f.run("agent", "control", "send", "--item", item, "--follow-up", "legacy followup")
	f.historyReady(item, 8)
	f.idle(item)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, f.wi, "agent", "control", "send", "--item", item, "--stdin", "--actor", "stdin-smoke")
	command.Env = f.env
	command.Dir = f.root
	command.Stdin = strings.NewReader("legacy stdin prompt")
	if out, err := command.CombinedOutput(); err != nil || !bytes.Contains(out, []byte("submitted")) {
		f.t.Fatalf("stdin send: %v %s", err, out)
	}
	f.historyReady(item, 10)
	f.idle(item)
	image := filepath.Join(f.root, "cli-image.png")
	if err := os.WriteFile(image, []byte("\x89PNG\r\n\x1a\nimage"), 0600); err != nil {
		f.t.Fatal(err)
	}
	f.run("agent", "control", "send", "--item", item, "--image", image)
	f.historyReady(item, 12)
	f.idle(item)
	f.run("agent", "control", "abort", "--item", item, "--actor", "smoke")
	f.run("agent", "monitor", item, "--limit", "1")
	f.run("agent", "monitor", item, "--limit", "1", "--json")
}
func (f *binaryFixture) cliTUICompatibility(repository string) {
	f.t.Run("CLI_TUI_entry_and_client_target", func(t *testing.T) {
		copy := *f
		copy.t = t
		f := &copy
		if _, err := exec.LookPath("tmux"); err != nil {
			t.Skip("tmux unavailable")
		}
		stub := filepath.Join(f.root, "client-bin")
		if err := os.Mkdir(stub, 0700); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(f.root, "client-targets")
		if err := os.WriteFile(filepath.Join(stub, "tmux"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$WI_CLI_TARGET_LOG\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		run := func(args ...string) {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, f.wi, args...)
			command.Dir = f.root
			command.Env = append(append([]string{}, f.env...), "PATH="+stub+string(os.PathListSeparator)+os.Getenv("PATH"), "WI_TMUX_CLIENT=owned-client", "WI_CLI_TARGET_LOG="+log)
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("%v: %v %s", args, err, out)
			}
		}
		var item v1.CreateItemResponse
		f.json(&item, "new", "--repo", repository, "--title", "CLI TUI", "--label", "cli-nav")
		id := item.Item.Id
		run("start", id)
		f.idle(id)
		run("switch", id)
		run("next", "--label", "cli-nav")
		f.run("agent", "runtime", "stop", "--item", id, "--force")
		run("resume", id)
		f.run("close", id, "--force")
		run("reopen", id, "--force")
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(data), "switch-client") != 5 || strings.Count(string(data), "owned-client") != 5 || !strings.Contains(string(data), filepath.Join(f.root, "tmux")) {
			t.Fatalf("terminal targeting: %s", data)
		}
		f.run("close", id, "--force")
	})
}
