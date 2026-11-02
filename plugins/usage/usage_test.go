package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/usage"
)

func usageCLIConfig(t *testing.T) Options {
	t.Helper()
	home := t.TempDir()
	return Options{Env: map[string]string{"HOME": home, "CODEX_HOME": filepath.Join(home, "codex"), "CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"), "XDG_CACHE_HOME": filepath.Join(home, "cache")}, Providers: []usage.Provider{stubUsageProvider{}}}
}

type stubUsageProvider struct{}

func (stubUsageProvider) Account() usage.Account {
	return usage.Account{Provider: "codex", ID: "fixture-slot"}
}
func (p stubUsageProvider) Quota(context.Context) usage.Quota {
	return usage.Quota{Account: p.Account(), State: usage.Unsupported, CheckedAt: time.Now().UTC(), Windows: []usage.Window{}}
}
func TestUsageWithoutAppDaemonOrNativeRuntime(t *testing.T) {
	cfg := usageCLIConfig(t)
	if err := os.MkdirAll(cfg.Env["CODEX_HOME"], 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Env["CODEX_HOME"], "auth.json"), []byte(`{"OPENAI_API_KEY":"fixture-never-output"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	cfg.Stdout = &out
	cfg.Stderr = &stderr
	code := Run(context.Background(), []string{"--provider", "codex", "--json"}, cfg)
	if code != ExitOK {
		t.Fatalf("%d %s", code, stderr.String())
	}
	var result struct {
		Version int `json:"schema_version"`
		Results []struct {
			Provider string            `json:"provider"`
			Account  string            `json:"account"`
			Error    *usage.QuotaError `json:"error"`
			Windows  []any             `json:"windows"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Version != 1 || len(result.Results) != 1 || result.Results[0].Provider != "codex" || result.Results[0].Account != "fixture-slot" || result.Results[0].Error == nil || result.Results[0].Error.Code != usage.Unsupported || result.Results[0].Windows == nil {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "fixture-never-output") {
		t.Fatal("secret leaked")
	}
	out.Reset()
	if code := Run(context.Background(), []string{"--provider", "codex"}, cfg); code != ExitOK {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), "Remaining subscription quota.") || !strings.Contains(out.String(), "Remaining quota unknown") {
		t.Fatal(out.String())
	}
}

type resolvingUsageProvider struct{ stubUsageProvider }

func (resolvingUsageProvider) Account() usage.Account {
	return usage.Account{Provider: "codex", ID: "before-resolution"}
}
func (resolvingUsageProvider) ResolveAccount(context.Context) usage.Provider {
	return stubUsageProvider{}
}

func TestUsageAccountFilterUsesResolvedNativeIdentity(t *testing.T) {
	cfg := usageCLIConfig(t)
	cfg.Providers = []usage.Provider{resolvingUsageProvider{}}
	var out, stderr bytes.Buffer
	cfg.Stdout, cfg.Stderr = &out, &stderr
	if code := Run(context.Background(), []string{"--provider", "codex", "--account", "fixture-slot", "--json"}, cfg); code != ExitOK {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), `"account": "fixture-slot"`) {
		t.Fatal(out.String())
	}
}

func TestUsageQuotaOnlyValidation(t *testing.T) {
	cfg := usageCLIConfig(t)
	var out, stderr bytes.Buffer
	cfg.Stdout = &out
	cfg.Stderr = &stderr
	for _, args := range [][]string{{"bogus"}, {"--watch"}, {"observe", "claude"}, {"--provider", "other"}, {"--account", "missing", "--provider", "codex"}, {"history"}, {"history", "--refresh"}, {"--since", "2026-10-01T00:00:00Z"}, {"--until", "2026-10-02T00:00:00Z"}} {
		if code := Run(context.Background(), args, cfg); code != ExitUsage {
			t.Errorf("%v: %d %s", args, code, stderr.String())
		}
	}
}
func TestUsageInstalledSourcesAndAliases(t *testing.T) {
	cfg := usageCLIConfig(t)
	cfg.Providers = nil
	cfg.Env["PATH"] = t.TempDir()
	cfg.Env["PI_CODING_AGENT_DIR"] = "ignored"
	var out, stderr bytes.Buffer
	cfg.Stdout = &out
	cfg.Stderr = &stderr
	if code := Run(context.Background(), []string{"--json"}, cfg); code != ExitOK {
		t.Fatal(code, stderr.String())
	}
	if !strings.Contains(out.String(), `"results": []`) {
		t.Fatal(out.String())
	}
	for alias, want := range map[string]string{"anthropic": "claude", "openai": "codex", "opencode": "opencode-go"} {
		out.Reset()
		if code := Run(context.Background(), []string{"--provider", alias, "--json"}, cfg); code != ExitOK {
			t.Fatal(code, stderr.String())
		}
		if !strings.Contains(out.String(), `"provider": "`+want+`"`) || !strings.Contains(out.String(), "not installed on PATH") {
			t.Fatal(out.String())
		}
	}
}
