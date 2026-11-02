package git_test

import (
	"context"
	"github.com/regb/workitem/internal/git"
	"path/filepath"
	"testing"
)

func TestIsAncestorIgnoresRepositoryEnvironment(t *testing.T) {
	intended, unrelated := setupRepo(t), setupRepo(t)
	runGit(t, intended, "commit", "--allow-empty", "-m", "unique intended commit")
	client := git.New("git")
	head, err := client.Head(context.Background(), intended)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(unrelated, ".git"))
	t.Setenv("GIT_WORK_TREE", unrelated)
	got, err := client.IsAncestor(context.Background(), intended, head, head)
	if err != nil || !got {
		t.Fatalf("ancestor=%v err=%v", got, err)
	}
}
