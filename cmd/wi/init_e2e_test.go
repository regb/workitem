package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/testutil"
)

func TestInitBootstrapsThroughBundledDrivers(t *testing.T) {
	temp := t.TempDir()
	binary := filepath.Join(temp, "wi")
	build := exec.Command("go", "build", "-o", binary, "./cmd/wi")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	tools := filepath.Join(temp, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(temp, "agent-invoked")
	for _, name := range []string{"pi", "claude"} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\necho invoked > "+marker+"\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	env := testutil.IsolatedEnvironment(os.Environ(), temp)
	env = append(env, "PATH="+tools)
	path := filepath.Join(temp, "config", "wi", "config.toml")
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = env
		cmd.Dir = temp
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("list"); err == nil || !strings.Contains(out, "default_agent") || !strings.Contains(out, "wi init") {
		t.Fatalf("missing default: %v %s", err, out)
	}
	if out, err := run("init", "--help"); err != nil || !strings.Contains(out, "--default") {
		t.Fatalf("bootstrap help: %v %s", err, out)
	}
	if out, err := run("init", "--default", "claude", "--json"); err != nil || !strings.Contains(out, `"default_agent":"claude"`) {
		t.Fatalf("init: %v %s", err, out)
	}
	cfg, _, err := config.Load(path)
	if err != nil || len(cfg.Agents) != 2 || cfg.DefaultAgent != "claude" || cfg.Agents["pi"].Config["executable"] != filepath.Join(tools, "pi") {
		t.Fatalf("cfg=%+v %v", cfg, err)
	}
	before, _ := os.ReadFile(path)
	if out, err := run("init", "--default", "pi"); err == nil || !strings.Contains(out, "already exists") {
		t.Fatalf("overwrite: %v %s", err, out)
	}
	// Inspection uses the config only, even inside a project with invalid
	// declarative settings and with only fake backends on PATH.
	if err := os.MkdirAll(filepath.Join(temp, ".config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(temp, ".config", "wi.toml"), []byte("not a valid project config"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, query := range []struct {
		args []string
		want string
	}{
		{[]string{"config", "agents", "--json"}, `"default_agent": "claude"`},
		{[]string{"config", "get", "default_agent"}, "claude\n"},
		{[]string{"config", "get", "deep_work.max_active", "--json"}, "2\n"},
		{[]string{"config", "get", "agents.pi.config.executable"}, "[redacted]\n"},
		{[]string{"config", "get", "agents.pi.config.executable", "--show-sensitive"}, filepath.Join(tools, "pi") + "\n"},
		{[]string{"config", "drivers", "--json"}, `"defaults"`},
		{[]string{"config", "plugins", "--json"}, `"direnv"`},
		{[]string{"config", "get", "plugins.direnv.trust", "--json"}, "\"prompt\"\n"},
		{[]string{"config", "--help"}, "Read-only"},
	} {
		if out, err := run(query.args...); err != nil || !strings.Contains(out, query.want) {
			t.Fatalf("config query %v: %v %s", query.args, err, out)
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("overwrote config")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("discovery or validation launched an agent")
	}
	if _, err := os.Stat(filepath.Join(temp, "data", "wi")); !os.IsNotExist(err) {
		t.Fatal("init created coordinator state")
	}
}
