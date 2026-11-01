package files_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/plugin"
	"github.com/regb/workitem/internal/plugins/files"
	"github.com/regb/workitem/internal/repofiles"
)

// checkout makes a bare store with one linked worktree, as wi's slots are.
func checkout(t *testing.T, tracked ...string) (store, worktree string) {
	t.Helper()
	root := t.TempDir()
	store, seed, worktree := filepath.Join(root, "store.git"), filepath.Join(root, "seed"), filepath.Join(root, "slot-0001")
	git := func(args ...string) {
		t.Helper()
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	git("init", "-q", "--bare", "-b", "main", store)
	git("init", "-q", "-b", "main", seed)
	for _, p := range append([]string{"README"}, tracked...) {
		if err := os.WriteFile(filepath.Join(seed, p), []byte("tracked\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("-C", seed, "add", ".")
	git("-C", seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	git("-C", seed, "push", "-q", store, "main")
	git("--git-dir", store, "worktree", "add", "-q", worktree, "main")
	return store, worktree
}

func run(t *testing.T, store, worktree, filesDir string, settings map[string]any, claim string) ([]string, error) {
	t.Helper()
	request := plugin.SetupRequest{Subject: plugin.Subject{CheckoutPath: worktree, ClaimID: claim}, RepositoryRoot: store, FilesDir: filesDir, Log: &bytes.Buffer{}}
	return files.New().SetupCheckout(context.Background(), request, settings)
}

func status(t *testing.T, worktree string) string {
	output, err := exec.Command("git", "-C", worktree, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestFilesAreCopiedAndIgnored(t *testing.T) {
	store, worktree := checkout(t)
	filesDir := filepath.Join(t.TempDir(), "files")
	for p, content := range map[string]string{".env": "A=1\n", "certs/dev.pem": "pem", "local.json": "{}"} {
		if _, err := repofiles.Put(filesDir, p, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(store, "info", "exclude"), []byte("# mine\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	warnings, err := run(t, store, worktree, filesDir, map[string]any{"skip": []any{"local.json"}}, "c1")
	if err != nil || len(warnings) != 0 {
		t.Fatalf("setup: %v %v", warnings, err)
	}
	if content, _ := os.ReadFile(filepath.Join(worktree, ".env")); string(content) != "A=1\n" {
		t.Fatalf(".env = %q", content)
	}
	if _, err := os.Stat(filepath.Join(worktree, "certs", "dev.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "local.json")); !os.IsNotExist(err) {
		t.Fatalf("skipped file copied: %v", err)
	}
	if s := status(t, worktree); s != "" {
		t.Fatalf("copies make the checkout dirty:\n%s", s)
	}
	exclude, _ := os.ReadFile(filepath.Join(store, "info", "exclude"))
	if !strings.HasPrefix(string(exclude), "# mine\n*.log\n") || !strings.Contains(string(exclude), "/.env\n/certs/dev.pem\n/local.json\n") {
		t.Fatalf("exclude =\n%s", exclude)
	}

	// The next claim of the slot gets the stored content back, and loses
	// copies of files since removed, but not an edited one.
	if err := os.WriteFile(filepath.Join(worktree, ".env"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := repofiles.Remove(filesDir, "certs/dev.pem"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, store, worktree, filesDir, nil, "c2"); err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(filepath.Join(worktree, ".env")); string(content) != "A=1\n" {
		t.Fatalf(".env = %q", content)
	}
	if _, err := os.Stat(filepath.Join(worktree, "certs", "dev.pem")); !os.IsNotExist(err) {
		t.Fatalf("removed file kept: %v", err)
	}
	exclude, _ = os.ReadFile(filepath.Join(store, "info", "exclude"))
	if strings.Contains(string(exclude), "dev.pem") {
		t.Fatalf("exclude keeps a removed file:\n%s", exclude)
	}
}

func TestATrackedFileShadowsTheCopy(t *testing.T) {
	store, worktree := checkout(t, ".env")
	filesDir := filepath.Join(t.TempDir(), "files")
	if _, err := repofiles.Put(filesDir, ".env", []byte("A=1\n")); err != nil {
		t.Fatal(err)
	}
	warnings, err := run(t, store, worktree, filesDir, nil, "c1")
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "shadowed") {
		t.Fatalf("warnings = %v, %v", warnings, err)
	}
	if content, _ := os.ReadFile(filepath.Join(worktree, ".env")); string(content) != "tracked\n" {
		t.Fatalf("tracked .env replaced: %q", content)
	}
}

func TestACopyNeverFollowsASymlink(t *testing.T) {
	store, worktree := checkout(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(worktree, "config")); err != nil {
		t.Fatal(err)
	}
	filesDir := filepath.Join(t.TempDir(), "files")
	if _, err := repofiles.Put(filesDir, "config/secret", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, store, worktree, filesDir, nil, "c1"); err == nil {
		t.Fatal("copied through a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "secret")); !os.IsNotExist(err) {
		t.Fatalf("wrote outside the checkout: %v", err)
	}
}
