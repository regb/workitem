package daemonapi

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRemoteDefaultBranchIgnoresRepositoryEnvironment(t *testing.T) {
	repo, unrelated := t.TempDir(), t.TempDir()
	for _, entry := range []struct{ path, branch string }{{repo, "intended"}, {unrelated, "wrong"}} {
		if out, err := exec.Command("git", "init", "--bare", "-b", entry.branch, entry.path).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	// ls-remote needs an existing HEAD ref.
	seed := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "intended", seed}, {"-C", seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-m", "init"}, {"-C", seed, "push", repo, "intended"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	t.Setenv("GIT_DIR", unrelated)
	t.Setenv("GIT_WORK_TREE", filepath.Join(unrelated, "work"))
	got, err := remoteDefaultBranch(context.Background(), repo)
	if err != nil || got != "intended" {
		t.Fatalf("branch=%s err=%v", got, err)
	}
}
