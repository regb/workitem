package daemonapi_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestContextHistoryAndCommitSelectionStayWithinThreeBranches(t *testing.T) {
	f, item, checkout := reviewWorkspace(t)
	ctx := context.Background()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Graph test", "-c", "user.email=graph@example.invalid", "-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	root := git(checkout.Claim.Path, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(root) {
		root = filepath.Join(checkout.Claim.Path, root)
	}
	integration := git(root, "symbolic-ref", "--short", "HEAD")
	base := git(root, "rev-parse", checkout.Branch)
	tree := git(root, "rev-parse", base+"^{tree}")
	local := git(root, "commit-tree", tree, "-p", base, "-m", "local integration work")
	remote := git(root, "commit-tree", tree, "-p", base, "-m", "remote-only work")
	other := git(root, "commit-tree", tree, "-p", base, "-m", "unrelated work")
	git(root, "update-ref", "refs/heads/"+integration, local)
	git(root, "update-ref", "refs/heads/unrelated", other)
	git(root, "update-ref", "refs/remotes/upstream/main", remote)
	git(root, "remote", "add", "upstream", "https://example.invalid/no-network.git")
	git(root, "config", "branch."+integration+".remote", "upstream")
	git(root, "config", "branch."+integration+".merge", "refs/heads/main")
	legacy, err := f.Clients.Reviews.ListReviewCommits(ctx, connect.NewRequest(&v1.ListReviewCommitsRequest{ItemId: item}))
	if err != nil || len(legacy.Msg.Commits) != 0 {
		t.Fatalf("legacy history changed: %v %v", legacy, err)
	}
	if _, err := f.Clients.Workspaces.ReleaseWorkspace(ctx, connect.NewRequest(&v1.ReleaseWorkspaceRequest{ItemId: item})); err != nil {
		t.Fatal(err)
	}
	graph, err := f.Clients.Reviews.ListReviewCommits(ctx, connect.NewRequest(&v1.ListReviewCommitsRequest{ItemId: item, IncludeContext: true}))
	if err != nil || len(graph.Msg.Commits) != 3 || len(graph.Msg.Branches) != 3 {
		t.Fatalf("graph without checkout: %v %v", graph, err)
	}
	for _, commit := range graph.Msg.Commits {
		if commit.Sha == other {
			t.Fatal("unrelated branch included")
		}
		if commit.Sha != base && (len(commit.ParentShas) != 1 || commit.ParentShas[0] != base) {
			t.Fatal("missing ancestry", commit)
		}
	}
	for _, sha := range []string{base, local, remote} {
		if _, err := f.Clients.Reviews.GetReview(ctx, connect.NewRequest(&v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: sha})); err != nil {
			t.Fatalf("context commit %s: %v", sha, err)
		}
	}
	custom, err := f.Clients.Reviews.GetReview(ctx, connect.NewRequest(&v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: local, BaseCommit: remote}))
	if err != nil || custom.Msg.Revision.BaseCommit != remote || custom.Msg.Revision.HeadCommit != local {
		t.Fatalf("custom range: %v %v", custom, err)
	}
	standard, err := f.Clients.Reviews.GetReview(ctx, connect.NewRequest(&v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: local}))
	if err != nil {
		t.Fatal(err)
	}
	file := &v1.GetReviewFileRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: local, BaseCommit: remote, Path: "missing.txt", ExpectedVersion: standard.Msg.Revision.Version}
	if _, err := f.Clients.Reviews.GetReviewFile(ctx, connect.NewRequest(file)); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("base change was not fenced: %v", err)
	}
	file.ExpectedVersion = custom.Msg.Revision.Version
	if _, err := f.Clients.Reviews.GetReviewFile(ctx, connect.NewRequest(file)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{other, "HEAD", "--all"} {
		if _, err := f.Clients.Reviews.GetReview(ctx, connect.NewRequest(&v1.GetReviewRequest{ItemId: item, BaseCommit: bad})); err == nil {
			t.Fatalf("unapproved base accepted: %s", bad)
		}
	}
	_, err = f.Clients.Reviews.GetReview(ctx, connect.NewRequest(&v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: other}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unrelated commit accepted: %v", err)
	}
}
