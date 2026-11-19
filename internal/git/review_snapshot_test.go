package git_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/regb/workitem/internal/git"
)

func TestReviewSnapshotPreservesUserHistoryAndIndex(t *testing.T) {
	dir := setupRepo(t)
	g := gitpkg.New("git")
	ctx := context.Background()
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("tracked.txt", "base\n")
	write(".gitignore", "ignored.txt\n")
	write(".env", "old tracked value\n")
	runGit(t, dir, "add", "tracked.txt", ".gitignore", ".env")
	runGit(t, dir, "commit", "-m", "base")
	head := strings.TrimSpace(runGitOutput(t, dir, "rev-parse", "HEAD"))
	branch := strings.TrimSpace(runGitOutput(t, dir, "branch", "--show-current"))
	write("tracked.txt", "staged\n")
	runGit(t, dir, "add", "tracked.txt")
	write("tracked.txt", "latest\n")
	write("new file.txt", "new\n")
	write("ignored.txt", "never persist\n")
	write(".env", "updated tracked configuration\n")
	write("-odd\nname.txt", "literal\n")
	if err := os.Symlink("/outside/secret", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	beforeStatus := runGitOutput(t, dir, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	beforeIndex, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", "/does/not/exist")
	t.Setenv("GIT_INDEX_FILE", "/does/not/exist")
	snap, err := g.CaptureReviewSnapshot(ctx, dir, head, branch)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", "")
	t.Setenv("GIT_INDEX_FILE", "")
	// Test helpers inherit env: remove repository selectors completely.
	os.Unsetenv("GIT_DIR")
	os.Unsetenv("GIT_INDEX_FILE")
	if got := runGitOutput(t, dir, "show", snap.Commit+":tracked.txt"); got != "latest" {
		t.Fatalf("snapshot %q", got)
	}
	if got := runGitOutput(t, dir, "show", snap.Commit+":new file.txt"); got != "new" {
		t.Fatal(got)
	}
	if got := runGitOutput(t, dir, "show", snap.Commit+":-odd\nname.txt"); got != "literal" {
		t.Fatal(got)
	}
	if got := runGitOutput(t, dir, "show", snap.Commit+":link"); got != "/outside/secret" {
		t.Fatal("followed symlink", got)
	}
	if got := runGitOutput(t, dir, "show", snap.Commit+":.env"); got != "updated tracked configuration" {
		t.Fatal("tracked configuration was not captured", got)
	}
	if len(snap.Excluded) != 0 {
		t.Fatalf("exclusions: %+v", snap.Excluded)
	}
	if tree := runGitOutput(t, dir, "ls-tree", "-r", snap.Commit); strings.Contains(tree, "ignored.txt") {
		t.Fatal("captured ignored path")
	}
	afterIndex, _ := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if !bytes.Equal(beforeIndex, afterIndex) || runGitOutput(t, dir, "status", "--porcelain=v2", "-z", "--untracked-files=all") != beforeStatus || strings.TrimSpace(runGitOutput(t, dir, "rev-parse", "HEAD")) != head {
		t.Fatal("capture changed branch, staging or status")
	}
	const item = "01M6KCHC5WTQ13SJZZ2EBM144C"
	ref := "refs/wi/review/" + item + "/snapshots/first"
	if err := g.PublishReviewSnapshot(ctx, dir, ref, snap.Commit); err != nil {
		t.Fatal(err)
	}
	if err := g.PublishReviewSnapshot(ctx, dir, ref, snap.Commit); err == nil {
		t.Fatal("snapshot ref overwritten")
	}
	runGit(t, dir, "gc", "--prune=now")
	if got := strings.TrimSpace(runGitOutput(t, dir, "rev-parse", ref)); got != snap.Commit {
		t.Fatal("lost retained snapshot")
	}
	if got := strings.TrimSpace(runGitOutput(t, dir, "rev-parse", snap.Commit+"^")); got != head {
		t.Fatal("snapshot parent is not observed HEAD")
	}
}

func TestReadAbsentReviewAndRejectStaleHead(t *testing.T) {
	dir := setupRepo(t)
	g := gitpkg.New("git")
	ctx := context.Background()
	if _, err := g.CaptureReviewSnapshot(ctx, dir, strings.Repeat("a", 40), ""); err == nil {
		t.Fatal("stale HEAD captured")
	}
}
