package app_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	gitpkg "github.com/regb/workitem/internal/git"
)

func mergeWorktreePaths(t *testing.T, repo string) string {
	t.Helper()
	var paths []string
	for _, line := range strings.Split(mergeGitOutput(t, repo, "worktree", "list", "--porcelain"), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, line)
		}
	}
	return strings.Join(paths, "\n")
}

type mergeSourceMove struct {
	gitpkg.Client
	source string
}

func (g mergeSourceMove) SquashCommit(ctx context.Context, dir, tree, parent, message string) (string, error) {
	commit, err := g.Client.SquashCommit(ctx, dir, tree, parent, message)
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "git", "-C", g.source, "commit", "--allow-empty", "-m", "concurrent source commit").CombinedOutput()
	if err != nil {
		return "", errors.New(string(out))
	}
	return commit, nil
}
func TestMergeRejectsConcurrentSourceCommitWithoutReset(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, source, "feature.txt", "retained leftover\n")
	a.Git = mergeSourceMove{Client: gitpkg.New("git"), source: source}
	target := mergeGitOutput(t, repo, "rev-parse", "main")
	_, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err == nil || !strings.Contains(err.Error(), "source branch or HEAD changed") {
		t.Fatal(err)
	}
	if mergeGitOutput(t, source, "log", "-1", "--format=%s") != "concurrent source commit" || mergeGitOutput(t, repo, "rev-parse", "main") != target || readMergeFile(t, source, "feature.txt") != "retained leftover\n" {
		t.Fatal("concurrent source work reset or target moved")
	}
}

type mergeCancelled struct {
	gitpkg.Client
	cancel context.CancelFunc
}

func (g mergeCancelled) RebaseOnto(ctx context.Context, _, _ string) error {
	g.cancel()
	return ctx.Err()
}
func TestMergeCancellationRetainsCommittedLeftoversWithoutTemporaryWorktree(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, repo, "target.txt", "target")
	runMergeGit(t, repo, "add", ".")
	runMergeGit(t, repo, "commit", "-m", "advance")
	writeMergeFile(t, source, "leftover.txt", "keep")
	before := mergeGitOutput(t, repo, "rev-parse", "main")
	worktrees := mergeWorktreePaths(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Git = mergeCancelled{Client: gitpkg.New("git"), cancel: cancel}
	result, err := a.MergeWorkItem(ctx, app.MergeOptions{Selector: item})
	if err == nil || !result.SourceCommitted || result.TargetAdvanced {
		t.Fatal(result, err)
	}
	if mergeGitOutput(t, repo, "rev-parse", "main") != before || mergeWorktreePaths(t, repo) != worktrees || mergeGitOutput(t, source, "show", "HEAD:leftover.txt") != "keep" {
		t.Fatal("cancellation lost source or leaked a temporary checkout")
	}
}
func TestMergeCommitsLeftoverKindsBeforeIntegration(t *testing.T) {
	for _, kind := range []string{"untracked", "unstaged", "staged", "mixed", "deleted", "renamed", "ignored", "ignored-only"} {
		t.Run(kind, func(t *testing.T) {
			a, repo, source, item := setupMergeWorkItem(t)
			wantPath, want := "README.md", "updated\n"
			switch kind {
			case "untracked":
				wantPath = "new.txt"
				writeMergeFile(t, source, wantPath, want)
			case "unstaged":
				writeMergeFile(t, source, wantPath, want)
			case "staged":
				writeMergeFile(t, source, wantPath, want)
				runMergeGit(t, source, "add", wantPath)
			case "mixed":
				writeMergeFile(t, source, wantPath, "staged\n")
				runMergeGit(t, source, "add", wantPath)
				writeMergeFile(t, source, wantPath, want)
			case "deleted":
				if err := os.Remove(source + "/README.md"); err != nil {
					t.Fatal(err)
				}
			case "renamed":
				runMergeGit(t, source, "mv", "README.md", "renamed.md")
				wantPath, want = "renamed.md", "initial\n"
			case "ignored", "ignored-only":
				writeMergeFile(t, source, ".gitignore", "local.bin\n")
				if kind == "ignored-only" {
					runMergeGit(t, source, "add", ".gitignore")
					runMergeGit(t, source, "commit", "-m", "ignore local data")
				}
				writeMergeFile(t, source, "local.bin", "keep local\n")
				wantPath, want = "README.md", "initial\n"
			}
			result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
			if err != nil {
				t.Fatal(err)
			}
			if mergeGitOutput(t, source, "status", "--porcelain") != "" {
				t.Fatal("leftovers not committed")
			}
			if kind == "deleted" {
				if _, err := os.Stat(repo + "/README.md"); !os.IsNotExist(err) {
					t.Fatal("deletion not integrated")
				}
			} else if readMergeFile(t, repo, wantPath) != want {
				t.Fatal("wrong integrated contents", kind, result)
			}
			if strings.HasPrefix(kind, "ignored") && readMergeFile(t, source, "local.bin") != "keep local\n" {
				t.Fatal("ignored data lost")
			}
		})
	}
}
func TestMergeOverlappingDirtyTargetAfterRebaseRetainsBothCheckouts(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, source, "README.md", "source\n")
	writeMergeFile(t, repo, "target.txt", "target")
	runMergeGit(t, repo, "add", ".")
	runMergeGit(t, repo, "commit", "-m", "advance target")
	before := mergeGitOutput(t, repo, "rev-parse", "main")
	writeMergeFile(t, repo, "README.md", "keep target WIP\n")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err == nil || !result.Rebased || result.TargetAdvanced {
		t.Fatal(result, err)
	}
	if mergeGitOutput(t, repo, "rev-parse", "main") != before || readMergeFile(t, repo, "README.md") != "keep target WIP\n" || readMergeFile(t, source, "README.md") != "source\n" {
		t.Fatal("overlap lost work")
	}
}
