package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/git"
)

func TestDetectRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := setupRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	info, err := git.New("git").DetectRepository(context.Background(), sub, "")
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(t, info.Repository.RootAtCreation, repo) {
		t.Fatalf("root = %q, want %q", info.Repository.RootAtCreation, repo)
	}
	if !filepath.IsAbs(info.Repository.GitCommonDir) || !strings.HasSuffix(info.Repository.GitCommonDir, ".git") {
		t.Fatalf("git common dir = %q", info.Repository.GitCommonDir)
	}
	if len(info.Repository.CreatedFromCommit) != 40 || info.Commit != info.Repository.CreatedFromCommit {
		t.Fatalf("commit = %q created from = %q", info.Commit, info.Repository.CreatedFromCommit)
	}
}

func TestWorktreeSnapshotCombinesHeadBranchAndStatus(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := setupRepo(t)
	client := git.New("git")
	head, branch, status, err := client.WorktreeSnapshot(context.Background(), repo)
	if err != nil || len(head) != 40 || branch == "" || status != "" {
		t.Fatalf("head=%q branch=%q status=%q err=%v", head, branch, status, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirtyHead, dirtyBranch, status, err := client.WorktreeSnapshot(context.Background(), repo)
	if err != nil || dirtyHead != head || dirtyBranch != branch || !strings.Contains(status, "untracked.txt") {
		t.Fatalf("head=%q branch=%q status=%q err=%v", dirtyHead, dirtyBranch, status, err)
	}
}

func TestRepositoryHomeReturnsPrimaryCheckoutFromLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := setupRepo(t)
	branch := strings.TrimSpace(runGitOutput(t, repo, "branch", "--show-current"))
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, repo, "worktree", "add", "-b", "feature", linked)
	home, err := git.New("git").RepositoryHome(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(t, home.Path, repo) || home.Branch != branch || home.Bare || home.Detached {
		t.Fatalf("home=%+v", home)
	}
}

func TestParseWorktreeList(t *testing.T) {
	got := git.ParseWorktreeList("worktree /repo\nHEAD abc\nbranch refs/heads/main\n\nworktree /tmp/feature\nHEAD def\nbranch refs/heads/feature\n\nworktree /tmp/detached\nHEAD fed\ndetached\n")
	if len(got) != 3 || got[0].Path != "/repo" || got[0].Branch != "main" || got[1].Branch != "feature" || !got[2].Detached {
		t.Fatalf("worktrees = %+v", got)
	}
}

func TestDefaultBranchWorktreeAndPlumbing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := setupRepo(t)
	client := git.New("git")
	ctx := context.Background()
	branch, err := client.DefaultBranch(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "master" && branch != "main" {
		t.Fatalf("default branch = %q", branch)
	}
	oldSHA, err := client.ResolveBranchSHA(ctx, repo, branch)
	if err != nil {
		t.Fatal(err)
	}
	feature := filepath.Join(t.TempDir(), "feature")
	if err := client.WorktreeAdd(ctx, git.WorktreeAddOptions{RepoRoot: repo, Path: feature, Branch: "feature", StartPoint: oldSHA, NewBranch: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(feature, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, feature, "add", "feature.txt")
	runGit(t, feature, "commit", "-m", "feature")
	newSHA, err := client.Head(ctx, feature)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.EnsureClean(ctx, feature); err != nil {
		t.Fatalf("clean checkout rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(feature, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.EnsureClean(ctx, feature); err == nil {
		t.Fatal("expected untracked checkout to be rejected")
	}
	if err := os.Remove(filepath.Join(feature, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	ancestor, err := client.IsAncestor(ctx, repo, oldSHA, newSHA)
	if err != nil || !ancestor {
		t.Fatalf("ancestor=%v err=%v", ancestor, err)
	}
	paths, err := client.DiffNameOnly(ctx, repo, oldSHA, newSHA)
	if err != nil || len(paths) != 1 || paths[0] != "feature.txt" {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	wt, err := client.TargetWorktreeForBranch(ctx, repo, "feature")
	if err != nil || wt == nil || !samePath(t, wt.Path, feature) {
		t.Fatalf("worktree=%+v err=%v", wt, err)
	}
	if err := client.UpdateRef(ctx, repo, "refs/heads/"+branch, newSHA, oldSHA, "test fast-forward"); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateRef(ctx, repo, "refs/heads/"+branch, oldSHA, oldSHA, "stale CAS"); err == nil {
		t.Fatal("expected compare-and-swap update to fail")
	}
}

func samePath(t *testing.T, left, right string) bool {
	t.Helper()
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func TestWorktreeAddRemoveAndBranchExists(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := setupRepo(t)
	client := git.New("git")
	ctx := context.Background()
	exists, err := client.BranchExists(ctx, repo, "wi/test")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("branch unexpectedly exists")
	}
	path := filepath.Join(t.TempDir(), "worktree")
	if err := client.WorktreeAdd(ctx, git.WorktreeAddOptions{RepoRoot: repo, Path: path, Branch: "wi/test", StartPoint: "HEAD", NewBranch: true}); err != nil {
		t.Fatal(err)
	}
	exists, err = client.BranchExists(ctx, repo, "wi/test")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("branch was not created")
	}
	if _, err := os.Stat(filepath.Join(path, "README.md")); err != nil {
		t.Fatalf("worktree content missing: %v", err)
	}
	branch, err := client.CurrentBranch(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "wi/test" {
		t.Fatalf("current branch = %q", branch)
	}
	if err := client.WorktreeRemove(ctx, repo, path, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists or unexpected stat error: %v", err)
	}
	if err := client.DeleteBranch(ctx, repo, "wi/test", true); err != nil {
		t.Fatal(err)
	}
	exists, err = client.BranchExists(ctx, repo, "wi/test")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("branch still exists after deletion")
	}
}

func setupRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")
	return repo
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}
