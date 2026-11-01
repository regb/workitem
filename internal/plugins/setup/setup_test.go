package setup_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/plugins/setup"
	"github.com/regb/workitem/internal/repofiles"
)

// repository makes a bare store whose main branch holds .config/wi.toml
// (when config is set), and a linked worktree on a feature branch.
func repository(t *testing.T, config, featureConfig string) (store, worktree string) {
	t.Helper()
	root := t.TempDir()
	store, seed, worktree := filepath.Join(root, "store.git"), filepath.Join(root, "seed"), filepath.Join(root, "slot-0001")
	git := func(args ...string) {
		t.Helper()
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	commit := func(content string) {
		if content != "" {
			if err := os.MkdirAll(filepath.Join(seed, ".config"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(seed, ".config", "wi.toml"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git("-C", seed, "add", ".")
		git("-C", seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c")
	}
	git("init", "-q", "--bare", "-b", "main", store)
	git("init", "-q", "-b", "main", seed)
	commit(config)
	git("-C", seed, "push", "-q", store, "main")
	git("-C", seed, "switch", "-q", "-c", "feature")
	commit(featureConfig)
	git("-C", seed, "push", "-q", store, "feature")
	git("--git-dir", store, "worktree", "add", "-q", worktree, "feature")
	return store, worktree
}

func run(t *testing.T, store, worktree, filesDir string, settings map[string]any) (string, []string, error) {
	t.Helper()
	log := &bytes.Buffer{}
	request := plugin.SetupRequest{
		Subject:        plugin.Subject{CheckoutPath: worktree},
		RepositoryRoot: store, DefaultBranch: "main", FilesDir: filesDir,
		Environment: map[string]string{"PATH": os.Getenv("PATH"), "WI_ID": "item"}, Log: log,
	}
	warnings, err := setup.New().SetupCheckout(context.Background(), request, settings)
	return log.String(), warnings, err
}

func TestTheConfiguredCommandRunsWithTheFiles(t *testing.T) {
	store, worktree := repository(t, "", "")
	filesDir := filepath.Join(t.TempDir(), "files")
	if _, err := repofiles.Put(filesDir, "template.env", []byte("A=1\n")); err != nil {
		t.Fatal(err)
	}
	log, _, err := run(t, store, worktree, filesDir, map[string]any{"run": `cp "$WI_FILES/template.env" .env && echo "set up $WI_ID in $(pwd)"`})
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	if content, _ := os.ReadFile(filepath.Join(worktree, ".env")); string(content) != "A=1\n" {
		t.Fatalf(".env = %q", content)
	}
	if !strings.Contains(log, "set up item in") {
		t.Fatalf("log =\n%s", log)
	}
	if _, _, err := run(t, store, worktree, filesDir, map[string]any{"run": "echo broken; exit 3"}); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("failure = %v", err)
	}
	if _, _, err := run(t, store, worktree, filesDir, map[string]any{"run": "sleep 5", "timeout_seconds": int64(1)}); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("timeout = %v", err)
	}
}

func TestADeclaredCommandRunsOnlyWhenTrusted(t *testing.T) {
	store, worktree := repository(t, "[workspace.setup]\nrun = \"touch from-main\"\n", "[workspace.setup]\nrun = \"touch from-feature\"\n")
	_, warnings, err := run(t, store, worktree, "", map[string]any{"trust": "never"})
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "not run") {
		t.Fatalf("untrusted: %v, %v", warnings, err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "from-main")); !os.IsNotExist(err) {
		t.Fatal("an untrusted declaration ran")
	}
	if _, _, err := run(t, store, worktree, "", map[string]any{"trust": "always"}); err != nil {
		t.Fatal(err)
	}
	// The default branch decides, never the checked-out branch.
	if _, err := os.Stat(filepath.Join(worktree, "from-main")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "from-feature")); !os.IsNotExist(err) {
		t.Fatal("the branch's declaration ran")
	}
}
