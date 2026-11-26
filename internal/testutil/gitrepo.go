package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Git runs git with args and returns its trimmed standard output, failing t
// when it fails.
func Git(t testing.TB, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v\n%s%s", args, err, output, stderr.String())
	}
	return strings.TrimSpace(string(output))
}

// GitBranch is one commit on a branch of GitStore, adding Files (relative path
// to content). A commit may add nothing.
type GitBranch struct {
	Name  string
	Files map[string]string
}

// GitStore creates a bare repository root/store.git, as wi's owned
// repositories are, and returns its path. Each branch holds one commit on top
// of the branch before it; the first branch is the repository's initial one.
func GitStore(t testing.TB, root string, branches ...GitBranch) string {
	t.Helper()
	store, seed := filepath.Join(root, "store.git"), filepath.Join(root, "seed")
	Git(t, "init", "-q", "--bare", "-b", branches[0].Name, store)
	Git(t, "init", "-q", "-b", branches[0].Name, seed)
	for i, branch := range branches {
		if i > 0 {
			Git(t, "-C", seed, "switch", "-q", "-c", branch.Name)
		}
		for name, content := range branch.Files {
			path := filepath.Join(seed, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		Git(t, "-C", seed, "add", ".")
		Git(t, "-C", seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", branch.Name)
		Git(t, "-C", seed, "push", "-q", store, branch.Name)
	}
	return store
}

// GitWorktree adds a linked worktree of store at path checking out commitish,
// as wi's managed slots are. Flags (such as "-b", "name") precede the path.
func GitWorktree(t testing.TB, store, path, commitish string, flags ...string) {
	t.Helper()
	args := append([]string{"--git-dir", store, "worktree", "add", "-q"}, flags...)
	Git(t, append(args, path, commitish)...)
}
