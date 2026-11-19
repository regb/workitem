package git_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/regb/workitem/internal/git"
)

func TestReviewCheckoutAuthorityIsRepositoryAndRootNotBranchName(t *testing.T) {
	dir := setupRepo(t)
	ctx := context.Background()
	g := gitpkg.New("git")
	repository := filepath.Join(dir, ".git")
	if err := g.ValidateReviewCheckout(ctx, repository, dir); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "switch", "-c", "topic/actual")
	if err := g.ValidateReviewCheckout(ctx, repository, dir); err != nil {
		t.Fatal("actual branch invalidated claim authority", err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := g.ValidateReviewCheckout(ctx, repository, nested); err == nil {
		t.Fatal("nested path was treated as claimed checkout root")
	}
	foreign := setupRepo(t)
	if err := g.ValidateReviewCheckout(ctx, repository, foreign); err == nil {
		t.Fatal("foreign repository accepted under original claim")
	}
}
func TestReviewSnapshotRejectsSameHeadBranchSwitchDuringCapture(t *testing.T) {
	dir := setupRepo(t)
	head := strings.TrimSpace(runGitOutput(t, dir, "rev-parse", "HEAD"))
	branch := strings.TrimSpace(runGitOutput(t, dir, "branch", "--show-current"))
	runGit(t, dir, "branch", "topic/switched", head)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "switch-on-write-tree")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := fmt.Sprintf("#!/bin/sh\nfor arg do\n if [ \"$arg\" = write-tree ] && [ -f %q ]; then\n rm %q\n %q -C %q symbolic-ref HEAD refs/heads/topic/switched || exit 1\n fi\ndone\nexec %q \"$@\"\n", marker, marker, git, dir, git)
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = gitpkg.New(wrapper).CaptureReviewSnapshot(context.Background(), dir, head, branch)
	if !errors.Is(err, gitpkg.ErrSnapshotChanged) {
		t.Fatalf("same-commit branch switch was not fenced: %v", err)
	}
	if got := strings.TrimSpace(runGitOutput(t, dir, "rev-parse", "HEAD")); got != head {
		t.Fatal("test changed HEAD instead of only branch identity")
	}
}
