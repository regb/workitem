package providers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/usage"
)

func TestDiscoveryInstalledSubscriptionClients(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": home, "PATH": bin, "PI_CODING_AGENT_DIR": "ignored-even-if-relative"}
	found, err := Discover(env, "")
	if err != nil || len(found) != 0 {
		t.Fatal(found, err)
	}
	for i, name := range []string{"claude", "codex", "opencode"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("must never run during discovery"), 0700); err != nil {
			t.Fatal(err)
		}
		found, err = Discover(env, "")
		if err != nil || len(found) != i+1 {
			t.Fatal(found, err)
		}
	}
}
func TestMissingClientsReturnUnknownQuota(t *testing.T) {
	env := map[string]string{"HOME": t.TempDir(), "PATH": t.TempDir()}
	for _, name := range []string{"claude", "codex", "opencode-go"} {
		found, err := Discover(env, name)
		if err != nil || len(found) != 1 {
			t.Fatal(found, err)
		}
		q := found[0].Quota(context.Background())
		if q.Account.Provider != name || q.State != usage.Unavailable || len(q.Windows) != 0 {
			t.Fatal(q)
		}
	}
}
func TestDiscoveryRootsAndAliases(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct{ key, provider string }{
		{"HOME", "claude"}, {"CLAUDE_CONFIG_DIR", "claude"}, {"CODEX_HOME", "codex"},
		{"OPENCODE_CONFIG_DIR", "opencode-go"}, {"XDG_DATA_HOME", "opencode-go"}, {"XDG_CONFIG_HOME", "opencode-go"},
	} {
		env := map[string]string{"HOME": home, tc.key: "relative"}
		if _, err := Discover(env, tc.provider); err == nil {
			t.Fatal(tc, "relative root accepted")
		}
	}
	if _, err := Discover(map[string]string{}, "claude"); err == nil {
		t.Fatal("missing HOME accepted")
	}
	for input, want := range map[string]string{"anthropic": "claude", "openai": "codex", "claude": "claude", "codex": "codex", "opencode": "opencode-go", "opencode-go": "opencode-go"} {
		if CanonicalProvider(input) != want {
			t.Fatal(input)
		}
	}
	if executable(map[string]string{"PATH": ".:relative"}, "claude") != "" {
		t.Fatal("relative PATH accepted")
	}
}
func TestSelectedProviderIgnoresOtherClientsConfiguration(t *testing.T) {
	env := map[string]string{"HOME": t.TempDir(), "CLAUDE_CONFIG_DIR": "relative", "OPENCODE_CONFIG_DIR": "relative"}
	if found, err := Discover(env, "codex"); err != nil || len(found) != 1 {
		t.Fatal(found, err)
	}
	// Uninstalled clients are ignored by default, including their invalid config.
	if found, err := Discover(env, ""); err != nil || len(found) != 0 {
		t.Fatal(found, err)
	}
}
func TestCacheRootMustBeAbsolute(t *testing.T) {
	if _, err := CacheRoot(map[string]string{"HOME": t.TempDir(), "XDG_CACHE_HOME": "relative"}); err == nil {
		t.Fatal("relative cache root accepted")
	}
	if root, err := CacheRoot(map[string]string{"HOME": t.TempDir()}); err != nil || !filepath.IsAbs(root) {
		t.Fatal(root, err)
	}
}
