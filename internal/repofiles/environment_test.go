package repofiles

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCheckoutGitIgnoresRepositoryEnvironment(t *testing.T) {
	root := t.TempDir()
	seed, store, checkout, unrelated := filepath.Join(root, "seed"), filepath.Join(root, "store.git"), filepath.Join(root, "checkout"), filepath.Join(root, "unrelated")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	run("init", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "intended"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	run("-C", seed, "add", ".")
	run("-C", seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
	run("clone", "--bare", seed, store)
	run("--git-dir", store, "worktree", "add", checkout, "main")
	run("init", unrelated)
	t.Setenv("GIT_DIR", filepath.Join(unrelated, ".git"))
	t.Setenv("GIT_WORK_TREE", unrelated)
	trees, err := Worktrees(context.Background(), store)
	if err != nil || len(trees) != 1 || filepath.Base(trees[0]) != filepath.Base(checkout) {
		t.Fatalf("trees=%v err=%v", trees, err)
	}
	paths, err := trackedPaths(context.Background(), checkout, []string{"intended"})
	if err != nil || !paths["intended"] {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
}
