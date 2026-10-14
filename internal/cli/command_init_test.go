package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/config"
)

type discoveryFixture struct {
	profiles []protocol.DiscoveredAgentConfig
	calls    int
}

func (f *discoveryFixture) DiscoverConfig(context.Context) (protocol.ConfigDiscoveryResult, error) {
	f.calls++
	return protocol.ConfigDiscoveryResult{Profiles: f.profiles}, nil
}
func (f *discoveryFixture) ValidateConfig(context.Context, string, json.RawMessage) error { return nil }
func initFixtures() map[string]configDiscoverer {
	return map[string]configDiscoverer{
		"pi":     &discoveryFixture{profiles: []protocol.DiscoveredAgentConfig{{Name: "pi", Label: "Pi", Config: json.RawMessage(`{"executable":"/opt/bin/pi","extensions":["/tmp/a b.ts"]}`)}}},
		"claude": &discoveryFixture{profiles: []protocol.DiscoveredAgentConfig{{Name: "claude", Label: "Claude Code", Config: json.RawMessage(`{"executable":"/opt/bin/claude","effort":"high"}`)}}},
	}
}
func TestInitSelectionAndConfigRoundTrip(t *testing.T) {
	for _, selection := range []string{"pi\n", "2\n", "invalid\npi\n"} {
		t.Run(strings.TrimSpace(selection), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wi", "config.toml")
			var out, errout bytes.Buffer
			if err := initialize(context.Background(), nil, path, initFixtures(), strings.NewReader(selection), &out, &errout, false); err != nil {
				t.Fatal(err)
			}
			cfg, warnings, err := config.Load(path)
			if err != nil || len(warnings) != 0 {
				t.Fatalf("%v %v", err, warnings)
			}
			if cfg.DefaultAgent != "pi" || cfg.Agents["pi"].Config["executable"] != "/opt/bin/pi" || len(cfg.Agents) != 2 {
				t.Fatalf("%+v", cfg.Agents)
			}
			stat, _ := os.Stat(path)
			if stat.Mode().Perm() != 0600 {
				t.Fatal(stat.Mode())
			}
			before, _ := os.ReadFile(path)
			if err := initialize(context.Background(), []string{"--default", "claude"}, path, initFixtures(), strings.NewReader(""), &out, &errout, false); err == nil {
				t.Fatal("overwrote config")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("config changed")
			}
		})
	}
}
func TestInitNoninteractiveAndNoSelection(t *testing.T) {
	for _, args := range [][]string{nil, {"--default", "missing"}, {"--unknown"}} {
		path := filepath.Join(t.TempDir(), "config.toml")
		var out bytes.Buffer
		if err := initialize(context.Background(), args, path, initFixtures(), strings.NewReader(""), &out, &out, false); err == nil {
			t.Fatal("expected selection error")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("config created on failure")
		}
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	var out, errout bytes.Buffer
	if err := initialize(context.Background(), []string{"--default", "claude"}, path, initFixtures(), strings.NewReader(""), &out, &errout, true); err != nil {
		t.Fatal(err)
	}
	var result struct {
		DefaultAgent string `json:"default_agent"`
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || result.DefaultAgent != "claude" {
		t.Fatal(out.String())
	}
	cfg, _, err := config.Load(path)
	if err != nil || cfg.DefaultAgent != "claude" {
		t.Fatalf("%+v %v", cfg, err)
	}
}
func TestInitNoAgentsOrMalformedDiscovery(t *testing.T) {
	for _, profiles := range [][]protocol.DiscoveredAgentConfig{nil, {{Name: "../escape", Config: json.RawMessage(`{}`)}}, {{Name: "pi", Config: json.RawMessage(`{"secret":{"nested":true}}`)}}} {
		path := filepath.Join(t.TempDir(), "config.toml")
		var out bytes.Buffer
		err := initialize(context.Background(), []string{"--default", "pi"}, path, map[string]configDiscoverer{"pi": &discoveryFixture{profiles: profiles}}, strings.NewReader(""), &out, &out, false)
		if err == nil {
			t.Fatal("expected discovery error")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("config created on discovery failure")
		}
	}
}
func TestInitRefusesSymlinkAndAtomicOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	path := filepath.Join(dir, "config.toml")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	fixture := &discoveryFixture{}
	if err := initialize(context.Background(), nil, path, map[string]configDiscoverer{"pi": fixture}, strings.NewReader(""), &out, &out, false); err == nil || fixture.calls != 0 {
		t.Fatal("existing symlink was not rejected before discovery")
	}
	if err := writeInitialConfig(path, []byte("replace")); err == nil {
		t.Fatal("atomic publication overwrote symlink")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("symlink target modified")
	}
}
