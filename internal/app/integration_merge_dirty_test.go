package app_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	gitpkg "github.com/regb/workitem/internal/git"
)

type mergeSourceSnapshot struct {
	Head, Status, Index, Staged, Unstaged string
	Files                                 map[string]string
}

func captureMergeSource(t *testing.T, path string) mergeSourceSnapshot {
	t.Helper()
	s := mergeSourceSnapshot{Head: mergeGitOutput(t, path, "rev-parse", "HEAD"), Status: mergeGitOutput(t, path, "status", "--porcelain=v1", "-uall"), Index: mergeGitOutput(t, path, "ls-files", "--stage"), Staged: mergeGitOutput(t, path, "diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv"), Unstaged: mergeGitOutput(t, path, "diff", "--binary", "--no-ext-diff", "--no-textconv"), Files: map[string]string{}}
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			s.Files[rel] = "symlink:" + target
			return err
		}
		data, err := os.ReadFile(p)
		s.Files[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func assertMergeSource(t *testing.T, path string, before mergeSourceSnapshot) {
	t.Helper()
	after := captureMergeSource(t, path)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("source checkout changed\nbefore=%+v\nafter=%+v", before, after)
	}
}
func mergeWorktreePaths(t *testing.T, repo string) string {
	t.Helper()
	paths := []string{}
	for _, line := range strings.Split(mergeGitOutput(t, repo, "worktree", "list", "--porcelain"), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, line)
		}
	}
	return strings.Join(paths, "\n")
}

func TestDirtyMergeFailuresPreserveOriginalCheckout(t *testing.T) {
	for _, failure := range []string{"conflict", "target moved", "target sync", "target overlap"} {
		t.Run(failure, func(t *testing.T) {
			application, repo, source, itemID := setupMergeWorkItem(t)
			writeMergeFile(t, source, "README.md", "committed feature\n")
			runMergeGit(t, source, "add", "README.md")
			runMergeGit(t, source, "commit", "-m", "feature")
			name := "target.txt"
			if failure == "conflict" {
				name = "README.md"
			}
			writeMergeFile(t, repo, name, "target committed\n")
			runMergeGit(t, repo, "add", name)
			runMergeGit(t, repo, "commit", "-m", "advance target")
			writeMergeFile(t, source, "README.md", "staged WIP\n")
			runMergeGit(t, source, "add", "README.md")
			writeMergeFile(t, source, "README.md", "unstaged WIP\n")
			writeMergeFile(t, source, "dataset.csv", "untracked WIP\n")
			if failure == "target moved" {
				application.Git = movedTargetGit{Client: gitpkg.New("git")}
			}
			if failure == "target sync" {
				application.Git = failingSyncGit{Client: gitpkg.New("git")}
			}
			if failure == "target overlap" {
				writeMergeFile(t, repo, "README.md", "target WIP\n")
			}
			before := captureMergeSource(t, source)
			target := mergeGitOutput(t, repo, "rev-parse", "main")
			paths := mergeWorktreePaths(t, repo)
			result, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, CWD: source})
			if err == nil || !result.RolledBack || result.TargetAdvanced {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			assertMergeSource(t, source, before)
			if got := mergeGitOutput(t, repo, "rev-parse", "main"); got != target {
				t.Fatal("target ref changed", got)
			}
			if got := mergeWorktreePaths(t, repo); got != paths {
				t.Fatal("temporary checkout leaked", got)
			}
			if failure == "target overlap" && readMergeFile(t, repo, "README.md") != "target WIP\n" {
				t.Fatal("target WIP changed")
			}
		})
	}
}

type changedSnapshotSourceGit struct {
	gitpkg.Client
	source string
}

func (g changedSnapshotSourceGit) RebaseMergeSnapshot(ctx context.Context, path, target string) error {
	if err := g.Client.RebaseMergeSnapshot(ctx, path, target); err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, "git", "-C", g.source, "commit", "--allow-empty", "-m", "concurrent source commit").CombinedOutput()
	if err != nil {
		return fmt.Errorf("concurrent commit: %s: %w", output, err)
	}
	return nil
}

func TestMergeRejectsUnmergedIndexWithoutOperationMarker(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	base := mergeGitOutput(t, source, "rev-parse", "HEAD")
	writeMergeFile(t, source, "README.md", "feature\n")
	runMergeGit(t, source, "add", "README.md")
	runMergeGit(t, source, "commit", "-m", "feature")
	writeMergeFile(t, repo, "README.md", "target\n")
	runMergeGit(t, repo, "add", "README.md")
	runMergeGit(t, repo, "commit", "-m", "target")
	runMergeGit(t, source, "read-tree", "-m", base, "HEAD", "main")
	before := mergeGitOutput(t, source, "ls-files", "--stage")
	_, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, CWD: source})
	if err == nil || !strings.Contains(err.Error(), "unmerged entries") {
		t.Fatal("unmerged index was accepted", err)
	}
	if after := mergeGitOutput(t, source, "ls-files", "--stage"); after != before {
		t.Fatal("unmerged index changed")
	}
}

type cleanupFailureMergeGit struct {
	gitpkg.Client
	root, path string
	failTarget bool
}

func (g *cleanupFailureMergeGit) WorktreeRemove(ctx context.Context, root, path string, force bool) error {
	g.root, g.path = root, path
	return fmt.Errorf("injected cleanup failure")
}
func (g *cleanupFailureMergeGit) UpdateRef(ctx context.Context, root, ref, newSHA, oldSHA, message string) error {
	if g.failTarget && message == "wi merge: fast-forward" {
		return fmt.Errorf("injected target failure")
	}
	return g.Client.UpdateRef(ctx, root, ref, newSHA, oldSHA, message)
}
func TestDirtyMergeReportsRetainedTemporaryCheckout(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("target_failure=%t", fail), func(t *testing.T) {
			application, repo, source, itemID := setupMergeWorkItem(t)
			writeMergeFile(t, source, "feature.txt", "feature\n")
			runMergeGit(t, source, "add", "feature.txt")
			runMergeGit(t, source, "commit", "-m", "feature")
			writeMergeFile(t, repo, "target.txt", "target\n")
			runMergeGit(t, repo, "add", "target.txt")
			runMergeGit(t, repo, "commit", "-m", "target")
			writeMergeFile(t, source, "data.csv", "keep\n")
			git := &cleanupFailureMergeGit{Client: gitpkg.New("git"), failTarget: fail}
			application.Git = git
			t.Cleanup(func() {
				if git.path != "" {
					if err := git.Client.WorktreeRemove(context.Background(), git.root, git.path, true); err != nil {
						t.Error(err)
					}
					if err := os.RemoveAll(filepath.Dir(git.path)); err != nil {
						t.Error(err)
					}
				}
			})
			result, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, CWD: source})
			if (err != nil) != fail || git.path == "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !strings.Contains(strings.Join(result.Warnings, "\n"), git.path) {
				t.Fatal("missing retained checkout warning", result)
			}
			if fail && !strings.Contains(err.Error(), git.path) {
				t.Fatal("CLI error loses retained checkout path", err)
			}
			if _, err := os.Stat(git.path); err != nil {
				t.Fatal("retained checkout missing", err)
			}
		})
	}
}

type cancelledSnapshotGit struct {
	gitpkg.Client
	cancel context.CancelFunc
}

func (g cancelledSnapshotGit) RebaseMergeSnapshot(ctx context.Context, path, target string) error {
	g.cancel()
	return ctx.Err()
}
func TestDirtyMergeCleansTemporaryCheckoutAfterCancellation(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	writeMergeFile(t, source, "feature.txt", "feature\n")
	runMergeGit(t, source, "add", "feature.txt")
	runMergeGit(t, source, "commit", "-m", "feature")
	writeMergeFile(t, repo, "target.txt", "target\n")
	runMergeGit(t, repo, "add", "target.txt")
	runMergeGit(t, repo, "commit", "-m", "target")
	writeMergeFile(t, source, "data.csv", "local data\n")
	before := captureMergeSource(t, source)
	paths := mergeWorktreePaths(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	application.Git = cancelledSnapshotGit{Client: gitpkg.New("git"), cancel: cancel}
	result, err := application.MergeWorkItem(ctx, app.MergeOptions{Selector: itemID, CWD: source})
	if err == nil || !result.RolledBack {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertMergeSource(t, source, before)
	if after := mergeWorktreePaths(t, repo); after != paths {
		t.Fatal("cancelled merge left a temporary checkout", after)
	}
}

func TestDirtyMergeRejectsConcurrentSourceCommitWithoutReset(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	writeMergeFile(t, source, "feature.txt", "feature\n")
	runMergeGit(t, source, "add", "feature.txt")
	runMergeGit(t, source, "commit", "-m", "feature")
	writeMergeFile(t, repo, "target.txt", "target\n")
	runMergeGit(t, repo, "add", "target.txt")
	runMergeGit(t, repo, "commit", "-m", "target")
	writeMergeFile(t, source, "dataset.csv", "keep data\n")
	old := mergeGitOutput(t, source, "rev-parse", "HEAD")
	target := mergeGitOutput(t, repo, "rev-parse", "main")
	application.Git = changedSnapshotSourceGit{Client: gitpkg.New("git"), source: source}
	result, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, CWD: source})
	if err == nil || !strings.Contains(err.Error(), "source branch or HEAD changed") || !result.RolledBack {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := mergeGitOutput(t, source, "rev-parse", "HEAD"); got == old {
		t.Fatal("concurrent source commit was reset")
	}
	if got := mergeGitOutput(t, repo, "rev-parse", "main"); got != target {
		t.Fatal("target changed")
	}
	if readMergeFile(t, source, "dataset.csv") != "keep data\n" {
		t.Fatal("source data changed")
	}
}
