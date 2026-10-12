package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	gitpkg "github.com/regb/workitem/internal/git"
)

type noRebaseGit struct{ gitpkg.Client }

type fastForwardFailureGit struct {
	noRebaseGit
	failSync bool
}

func (g fastForwardFailureGit) UpdateRef(ctx context.Context, root, ref, newSHA, oldSHA, message string) error {
	if !g.failSync && message == "wi merge: fast-forward" {
		return errors.New("injected target movement")
	}
	return g.Client.UpdateRef(ctx, root, ref, newSHA, oldSHA, message)
}

func (g fastForwardFailureGit) ReadTreeMergeUpdate(context.Context, string, string, string) error {
	return errors.New("injected target sync failure")
}

type ancestryFailureGit struct{ noRebaseGit }

func (ancestryFailureGit) IsAncestor(context.Context, string, string, string) (bool, error) {
	return false, errors.New("injected ancestry check failure")
}

func TestMergeWorkItemFastForwardFailuresDoNotResetSource(t *testing.T) {
	for _, failure := range []string{"target movement", "target sync", "dirty overlap"} {
		t.Run(failure, func(t *testing.T) {
			application, repo, source, itemID := setupMergeWorkItem(t)
			git := noRebaseGit{Client: gitpkg.New("git")}
			application.Git = git
			if failure != "dirty overlap" {
				application.Git = fastForwardFailureGit{noRebaseGit: git, failSync: failure == "target sync"}
			}
			writeMergeFile(t, source, "README.md", "source change\n")
			runMergeGit(t, source, "add", "README.md")
			runMergeGit(t, source, "commit", "-m", "source change")
			sourceOld := mergeGitOutput(t, source, "rev-parse", "HEAD")
			targetOld := mergeGitOutput(t, repo, "rev-parse", "main")
			if failure == "dirty overlap" {
				writeMergeFile(t, repo, "README.md", "keep dirty\n")
			}
			res, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, CWD: source})
			if err == nil || !res.RolledBack || res.Rebased || res.TargetAdvanced || len(res.Warnings) != 0 {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			if got := mergeGitOutput(t, source, "rev-parse", "HEAD"); got != sourceOld {
				t.Fatalf("source changed: %s", got)
			}
			if got := mergeGitOutput(t, repo, "rev-parse", "main"); got != targetOld {
				t.Fatalf("target changed: %s", got)
			}
			if failure == "dirty overlap" && readMergeFile(t, repo, "README.md") != "keep dirty\n" {
				t.Fatal("dirty target content changed")
			}
		})
	}
}

func TestMergeWorkItemFailsClosedWhenAncestryCheckFails(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	application.Git = ancestryFailureGit{noRebaseGit{Client: gitpkg.New("git")}}
	sourceOld := mergeGitOutput(t, source, "rev-parse", "HEAD")
	targetOld := mergeGitOutput(t, repo, "rev-parse", "main")
	res, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, CWD: source})
	if err == nil || !strings.Contains(err.Error(), "check target ancestry") || res.Rebased || res.TargetAdvanced {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if got := mergeGitOutput(t, source, "rev-parse", "HEAD"); got != sourceOld {
		t.Fatal("source changed after failed ancestry check")
	}
	if got := mergeGitOutput(t, repo, "rev-parse", "main"); got != targetOld {
		t.Fatal("target changed after failed ancestry check")
	}
}

func (noRebaseGit) Rebase(context.Context, string, string) error {
	return errors.New("unexpected rebase on fast-forward path")
}
func (noRebaseGit) ResetHard(context.Context, string, string) error {
	return errors.New("unexpected source reset on fast-forward path")
}

func TestMergeWorkItemFastForwardsWithoutRebase(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	application.Git = noRebaseGit{Client: gitpkg.New("git")}
	writeMergeFile(t, source, "feature.txt", "feature\n")
	runMergeGit(t, source, "add", "feature.txt")
	runMergeGit(t, source, "commit", "-m", "feature")
	sourceOld := mergeGitOutput(t, source, "rev-parse", "HEAD")
	res, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, CWD: source})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rebased || !res.TargetAdvanced || !res.TargetSynced || res.SourceOldSHA != sourceOld || res.SourceNewSHA != sourceOld {
		t.Fatalf("result=%+v", res)
	}
	if got := mergeGitOutput(t, repo, "rev-parse", "main"); got != sourceOld {
		t.Fatalf("main=%s want=%s", got, sourceOld)
	}
}

func TestMergeWorkItemFastForwardPreservesSubtreeMerge(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	writeMergeFile(t, source, "BUILD.bazel", "monorepo build\n")
	runMergeGit(t, source, "add", "BUILD.bazel")
	runMergeGit(t, source, "commit", "-m", "monorepo build")
	vendor := t.TempDir()
	runMergeGit(t, vendor, "init", "-b", "main")
	runMergeGit(t, vendor, "config", "user.name", "Test User")
	runMergeGit(t, vendor, "config", "user.email", "test@example.com")
	writeMergeFile(t, vendor, "BUILD.bazel", "vendor build\n")
	runMergeGit(t, vendor, "add", "BUILD.bazel")
	runMergeGit(t, vendor, "commit", "-m", "vendor root")
	// Construct a subtree merge using standard Git commands, without requiring
	// the optional git-subtree executable. The second parent has unprefixed files.
	runMergeGit(t, source, "fetch", vendor, "main")
	runMergeGit(t, source, "merge", "--allow-unrelated-histories", "-s", "ours", "--no-commit", "FETCH_HEAD")
	runMergeGit(t, source, "read-tree", "--prefix=vendor/open3/", "-u", "FETCH_HEAD")
	runMergeGit(t, source, "commit", "-m", "Vendor open3 subtree")
	sourceOld := mergeGitOutput(t, source, "rev-parse", "HEAD")
	parents := mergeGitOutput(t, source, "show", "-s", "--format=%P", "HEAD")
	if len(strings.Fields(parents)) != 2 {
		t.Fatal("fixture is not a subtree merge")
	}
	res, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, CWD: source})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rebased || res.SourceNewSHA != sourceOld || !res.TargetAdvanced || !res.TargetSynced {
		t.Fatalf("result=%+v", res)
	}
	if got := mergeGitOutput(t, source, "rev-parse", "HEAD"); got != sourceOld {
		t.Fatalf("source was rewritten: %s", got)
	}
	if got := mergeGitOutput(t, repo, "show", "-s", "--format=%P", "main"); got != parents {
		t.Fatalf("merge parents changed: %s", got)
	}
	for _, root := range []string{source, repo} {
		if got := readMergeFile(t, root, "BUILD.bazel"); got != "monorepo build\n" {
			t.Fatalf("root build was replaced: %q", got)
		}
		if got := readMergeFile(t, root, filepath.Join("vendor", "open3", "BUILD.bazel")); got != "vendor build\n" {
			t.Fatalf("vendor build=%q", got)
		}
	}
	events, err := application.Store.ReadEvents(itemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "merge.rebased" {
			t.Fatal("fast-forward emitted a rebase event")
		}
	}
}
