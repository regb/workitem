package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func configQueryFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	text := `default_agent = "pi"
[agents.pi]
driver = "pi"
label = "My Pi"
[agents.pi.config]
model = "private-model"
api_key = "profile-secret"
extensions = ["private-extension"]
[agents.web-search]
driver = "pi"
[drivers.pi]
command = "/private/driver --token command-secret"
[drivers.pi.defaults]
credential = "default-secret"
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigQueryDefaultsScalarsAndSections(t *testing.T) {
	path := configQueryFixture(t)
	for _, tt := range []struct {
		args []string
		json bool
		want string
	}{
		{[]string{"get", "default_agent"}, false, "pi\n"},
		{[]string{"get", "agent.append_system_prompt"}, false, "This conversation belongs to a Wi work item."},
		{[]string{"get", "default_agent"}, true, "\"pi\"\n"},
		{[]string{"get", "deep_work.max_active"}, true, "2\n"},
		{[]string{"get", "agents.pi.label"}, false, "My Pi\n"},
		{[]string{"get", "plugins.direnv.trust"}, true, "\"prompt\"\n"},
		{[]string{"get", "drivers.claude.defaults"}, true, "{}\n"},
		{[]string{"get", "agents.web-search.config"}, true, "{}\n"},
		{[]string{"get", "agent"}, false, "agent.startup_message = "},
		{nil, false, "attention.priority = \"recent-request\""},
	} {
		t.Run(strings.Join(tt.args, "/")+tt.want, func(t *testing.T) {
			var out, errout bytes.Buffer
			if code := runConfigMain(tt.args, path, &out, &errout, tt.json); code != ExitOK || !strings.Contains(out.String(), tt.want) {
				t.Fatalf("code=%d out=%s err=%s", code, &out, &errout)
			}
		})
	}
}

func TestConfigQueriesConfiguredSystemPrompt(t *testing.T) {
	path := configQueryFixture(t)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString("\n[agent]\nappend_system_prompt = \"Custom system instructions.\"\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("write=%v close=%v", writeErr, closeErr)
	}
	var out, errout bytes.Buffer
	if code := runConfigMain([]string{"get", "agent.append_system_prompt"}, path, &out, &errout, true); code != ExitOK || out.String() != "\"Custom system instructions.\"\n" {
		t.Fatalf("%d %s %s", code, &out, &errout)
	}
}

func TestConfigQueryOpaqueValuesAreOptIn(t *testing.T) {
	path := configQueryFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"show"}, {"get", "agents.pi.config"}, {"drivers"}, {"get", "drivers.pi.command"}, {"agents"}} {
		for _, jsonOut := range []bool{false, true} {
			var out, errout bytes.Buffer
			if code := runConfigMain(args, path, &out, &errout, jsonOut); code != ExitOK {
				t.Fatalf("%v code=%d %s", args, code, &errout)
			}
			for _, secret := range []string{"profile-secret", "private-model", "private-extension", "command-secret", "default-secret"} {
				if strings.Contains(out.String(), secret) {
					t.Fatalf("%v exposed %q: %s", args, secret, &out)
				}
			}
			if jsonOut && !json.Valid(out.Bytes()) {
				t.Fatalf("invalid JSON: %s", &out)
			}
		}
	}
	var out, errout bytes.Buffer
	if code := runConfigMain([]string{"get", "agents.pi.config.api_key", "--show-sensitive"}, path, &out, &errout, false); code != ExitOK || out.String() != "profile-secret\n" {
		t.Fatalf("%d %s %s", code, &out, &errout)
	}
	out.Reset()
	if code := runConfigMain([]string{"--show-sensitive", "show"}, path, &out, &errout, true); code != ExitOK || !strings.Contains(out.String(), "command-secret") || !strings.Contains(out.String(), "default-secret") {
		t.Fatalf("%d %s %s", code, &out, &errout)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("query changed config", err)
	}
}

func TestConfigAgentsAreSortedAndMarkDefault(t *testing.T) {
	var out, errout bytes.Buffer
	path := configQueryFixture(t)
	code := runConfigMain([]string{"agents"}, path, &out, &errout, true)
	var result struct {
		DefaultAgent string               `json:"default_agent"`
		Agents       []configAgentProfile `json:"agents"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || code != ExitOK {
		t.Fatalf("%v %d %s %s", err, code, &out, &errout)
	}
	if result.DefaultAgent != "pi" || len(result.Agents) != 2 || result.Agents[0].Name != "pi" || !result.Agents[0].Default || result.Agents[0].Label != "My Pi" || result.Agents[1].Default || result.Agents[1].Name != "web-search" {
		t.Fatalf("%+v", result)
	}
	out.Reset()
	if code := runConfigMain([]string{"agents"}, path, &out, &errout, false); code != ExitOK || !strings.Contains(out.String(), "DEFAULT") || !strings.Contains(out.String(), "*") {
		t.Fatalf("%d %s %s", code, &out, &errout)
	}
}

func TestConfigQueryWarningsStayOffJSONAndNumbersRemainExact(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("requires 64-bit integers")
	}
	path := configQueryFixture(t)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n[deep_work]\nmax_active = 9007199254740993\nunknown_setting = true\n")
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("write=%v close=%v", err, closeErr)
	}
	var out, errout bytes.Buffer
	if code := runConfigMain([]string{"get", "deep_work.max_active"}, path, &out, &errout, true); code != ExitOK || out.String() != "9007199254740993\n" || !strings.Contains(errout.String(), "warning: config:") {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errout)
	}
}

func TestConfigRejectsRepositoryPolicyFlags(t *testing.T) {
	path := configQueryFixture(t)
	var out, errout bytes.Buffer
	if code := runConfigMain([]string{"get", "plugins.direnv.trust", "--repo", "biskoui"}, path, &out, &errout, true); code != ExitUsage || !strings.Contains(errout.String(), "repo settings") {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errout)
	}
	out.Reset()
	errout.Reset()
	if code := runConfigMain([]string{"plugins"}, path, &out, &errout, true); code != ExitOK || !strings.Contains(out.String(), "\"repository\": true") {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errout)
	}
}

func TestConfigQueryErrors(t *testing.T) {
	path := configQueryFixture(t)
	for _, tt := range []struct {
		args []string
		code int
	}{
		{[]string{"edit"}, ExitUsage}, {[]string{"get"}, ExitUsage}, {[]string{"get", ""}, ExitUsage}, {[]string{"show", "extra"}, ExitUsage}, {[]string{"agents", "pi"}, ExitUsage}, {[]string{"--unknown"}, ExitUsage},
		{[]string{"get", "missing"}, ExitNotFound}, {[]string{"get", "deep_work.missing"}, ExitNotFound}, {[]string{"get", "default_agent.child"}, ExitNotFound}, {[]string{"get", "agents..driver"}, ExitNotFound},
	} {
		var out, errout bytes.Buffer
		if code := runConfigMain(tt.args, path, &out, &errout, true); code != tt.code || out.Len() != 0 || errout.Len() == 0 {
			t.Fatalf("%v code=%d want=%d out=%s err=%s", tt.args, code, tt.code, &out, &errout)
		}
	}
	for _, content := range []string{"default_agent = broken", "[deep_work]\nmax_active = -1"} {
		bad := filepath.Join(t.TempDir(), "bad.toml")
		if err := os.WriteFile(bad, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		var out, errout bytes.Buffer
		if code := runConfigMain([]string{"show"}, bad, &out, &errout, true); code != ExitError || out.Len() != 0 {
			t.Fatalf("code=%d out=%s err=%s", code, &out, &errout)
		}
	}
}
