package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/testutil"
)

type mergeNoRebase struct {
	gitpkg.Client
	called bool
}

func (g *mergeNoRebase) RebaseOnto(context.Context, string, string) error {
	g.called = true
	return errors.New("unexpected rebase")
}

type mergeAncestryFailure struct{ gitpkg.Client }

func (mergeAncestryFailure) IsAncestor(context.Context, string, string, string) (bool, error) {
	return false, errors.New("injected ancestry check failure")
}
func TestMergeFailsClosedWhenAncestryCheckFails(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	a.Git = mergeAncestryFailure{gitpkg.New("git")}
	head := mergeGitOutput(t, source, "rev-parse", "HEAD")
	target := mergeGitOutput(t, repo, "rev-parse", "main")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Rebase: true})
	if err == nil || !strings.Contains(err.Error(), "ancestry check failure") || result.TargetAdvanced || result.Rebased {
		t.Fatal(result, err)
	}
	if mergeGitOutput(t, source, "rev-parse", "HEAD") != head || mergeGitOutput(t, repo, "rev-parse", "main") != target {
		t.Fatal("ancestry failure changed refs")
	}
}
func TestMergeMissingRegisteredTargetRefusesBeforeRebase(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	target := filepath.Join(t.TempDir(), "missing")
	runMergeGit(t, repo, "worktree", "add", "-b", "develop", target, "main")
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	writeMergeFile(t, source, "feature.txt", "feature")
	runMergeGit(t, source, "add", ".")
	runMergeGit(t, source, "commit", "-m", "feature")
	head := mergeGitOutput(t, source, "rev-parse", "HEAD")
	g := &mergeNoRebase{Client: gitpkg.New("git")}
	a.Git = g
	_, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Target: "develop"})
	if err == nil || !strings.Contains(err.Error(), "unavailable worktree") || g.called {
		t.Fatal("missing worktree was not rejected before rebase", err)
	}
	if mergeGitOutput(t, source, "rev-parse", "HEAD") != head {
		t.Fatal("missing target changed source")
	}
}
func TestMergeSyncsManagedTargetWorktreeAndRecordsItsEvent(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	next := 110
	a.NewID = func() (string, error) { next++; return testutil.ID(t, byte(next)), nil }
	targetItem, err := a.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Managed target", CWD: repo})
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.StartWorkItem(context.Background(), app.ResolveOptions{Selector: targetItem.Manifest.ID, CWD: repo}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	targetPath := target.Workspace.Checkout.Claim.Path
	branch := target.Workspace.Checkout.Specification.Branch
	writeMergeFile(t, targetPath, "target.txt", "target commit\n")
	runMergeGit(t, targetPath, "add", ".")
	runMergeGit(t, targetPath, "commit", "-m", "target change")
	writeMergeFile(t, targetPath, "local.txt", "keep dirty target\n")
	writeMergeFile(t, source, "feature.txt", "feature\n")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Target: branch})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Rebased || !result.TargetSynced || result.TargetManagedWorkItemID != targetItem.Manifest.ID || readMergeFile(t, targetPath, "feature.txt") != "feature\n" || readMergeFile(t, targetPath, "local.txt") != "keep dirty target\n" {
		t.Fatal("managed target not synchronized safely", result)
	}
	events, err := a.Store.ReadEvents(targetItem.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type == "checkout.updated_by_merge" {
			found = true
			if event.Data["source_item_id"] != item || event.Data["new_sha"] != result.Commit {
				t.Fatal("incorrect managed target event", event)
			}
		}
	}
	if !found {
		t.Fatal("managed target event missing")
	}
}
func TestMergeExplicitTargetWithoutWorktree(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	runMergeGit(t, repo, "branch", "develop")
	writeMergeFile(t, source, "feature.txt", "feature")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Target: "develop"})
	if err != nil || !result.TargetAdvanced || result.TargetSynced || result.TargetWorktreePath != "" {
		t.Fatal(result, err)
	}
}
func TestMergeRebaseFastForwardPreservesSubtreeMerge(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	g := &mergeNoRebase{Client: gitpkg.New("git")}
	a.Git = g
	writeMergeFile(t, source, "BUILD.bazel", "monorepo build\n")
	runMergeGit(t, source, "add", ".")
	runMergeGit(t, source, "commit", "-m", "monorepo build")
	vendor := t.TempDir()
	runMergeGit(t, vendor, "init", "-b", "main")
	runMergeGit(t, vendor, "config", "user.name", "Test")
	runMergeGit(t, vendor, "config", "user.email", "test@example.invalid")
	writeMergeFile(t, vendor, "BUILD.bazel", "vendor build\n")
	runMergeGit(t, vendor, "add", ".")
	runMergeGit(t, vendor, "commit", "-m", "vendor root")
	runMergeGit(t, source, "fetch", vendor, "main")
	runMergeGit(t, source, "merge", "--allow-unrelated-histories", "-s", "ours", "--no-commit", "FETCH_HEAD")
	runMergeGit(t, source, "read-tree", "--prefix=vendor/open3/", "-u", "FETCH_HEAD")
	runMergeGit(t, source, "commit", "-m", "Vendor subtree")
	head := mergeGitOutput(t, source, "rev-parse", "HEAD")
	parents := mergeGitOutput(t, source, "show", "-s", "--format=%P", "HEAD")
	if len(strings.Fields(parents)) != 2 {
		t.Fatal("not a subtree merge")
	}
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Rebase: true})
	if err != nil || result.Commit != head || result.Rebased || g.called || !result.TargetSynced {
		t.Fatal(result, err)
	}
	if mergeGitOutput(t, repo, "show", "-s", "--format=%P", "main") != parents {
		t.Fatal("subtree parents changed")
	}
	for _, root := range []string{repo, source} {
		if readMergeFile(t, root, "BUILD.bazel") != "monorepo build\n" || readMergeFile(t, root, "vendor/open3/BUILD.bazel") != "vendor build\n" {
			t.Fatal("subtree content changed")
		}
	}
}

type mergeSyncFailure struct{ gitpkg.Client }

func (mergeSyncFailure) ReadTreeMergeUpdate(context.Context, string, string, string) error {
	return errors.New("injected target sync failure")
}
func TestMergeTargetSyncFailureRetainsPublicationAndSource(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, source, "feature.txt", "retained")
	a.Git = mergeSyncFailure{gitpkg.New("git")}
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err == nil || !result.TargetAdvanced || !result.Recorded || !result.SourceAdvanced {
		t.Fatal(result, err)
	}
	if mergeGitOutput(t, repo, "rev-parse", "main") != result.Commit || mergeGitOutput(t, source, "rev-parse", "HEAD") != result.Commit {
		t.Fatal("published result rolled back")
	}
}
func TestMergeRefusesCaptureInProgress(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	a.ReviewCaptures = app.NewReviewCaptureQueue(ctx, func(ctx context.Context, _ string) { close(entered); <-ctx.Done() })
	defer func() { cancel(); a.ReviewCaptures.Close() }()
	a.ReviewCaptures.Trigger(item)
	<-entered
	target := mergeGitOutput(t, repo, "rev-parse", "main")
	_, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	cancel()
	if err == nil || !strings.Contains(err.Error(), "capture is in progress") {
		t.Fatal(err)
	}
	if mergeGitOutput(t, source, "rev-parse", "HEAD") != target {
		t.Fatal("capture refusal changed source")
	}
}
func TestMergeRejectsUnmergedIndexWithoutOperationMarker(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	base := mergeGitOutput(t, source, "rev-parse", "HEAD")
	writeMergeFile(t, source, "README.md", "source\n")
	runMergeGit(t, source, "add", ".")
	runMergeGit(t, source, "commit", "-m", "source")
	writeMergeFile(t, repo, "README.md", "target\n")
	runMergeGit(t, repo, "add", ".")
	runMergeGit(t, repo, "commit", "-m", "target")
	runMergeGit(t, source, "read-tree", "-m", base, "HEAD", "main")
	before := mergeGitOutput(t, source, "ls-files", "--stage")
	if _, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item}); err == nil {
		t.Fatal("unmerged index accepted")
	}
	if mergeGitOutput(t, source, "ls-files", "--stage") != before {
		t.Fatal("unmerged index changed")
	}
}
