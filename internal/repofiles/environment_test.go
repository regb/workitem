package repofiles

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/testutil"
)

func TestCheckoutGitIgnoresRepositoryEnvironment(t *testing.T) {
	root := t.TempDir()
	checkout, unrelated := filepath.Join(root, "checkout"), filepath.Join(root, "unrelated")
	store := testutil.GitStore(t, root, testutil.GitBranch{Name: "main", Files: map[string]string{"intended": "ok"}})
	testutil.GitWorktree(t, store, checkout, "main")
	testutil.Git(t, "init", "-q", unrelated)
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
