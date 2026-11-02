package app

import (
	"context"
	"github.com/regb/workitem/internal/model"
	"os/exec"
	"testing"
)

func TestDefaultBranchIgnoresRepositoryEnvironment(t *testing.T) {
	intended, unrelated := t.TempDir(), t.TempDir()
	for _, repo := range []struct{ path, branch string }{{intended, "intended"}, {unrelated, "wrong"}} {
		if out, err := exec.Command("git", "init", "--bare", "-b", repo.branch, repo.path).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	t.Setenv("GIT_DIR", unrelated)
	t.Setenv("GIT_WORK_TREE", unrelated)
	if got := defaultBranch(context.Background(), model.Repository{GitCommonDir: intended, RootAtCreation: intended}); got != "intended" {
		t.Fatalf("branch=%s", got)
	}
}
