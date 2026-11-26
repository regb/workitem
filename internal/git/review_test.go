package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	gitpkg "github.com/regb/workitem/internal/git"
)

func TestReviewRecreatedUntrackedFileIsNotAnAddition(t *testing.T) {
	dir := setupRepo(t)
	run := func(args ...string) string { t.Helper(); return runGitOutput(t, dir, args...) }
	writeRepoFile(t, dir, "restored", "base")
	run("add", ".")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	run("rm", "restored")
	run("commit", "-m", "delete")
	writeRepoFile(t, dir, "restored", "changed")
	paths, _, err := gitpkg.New("git").ReviewFiles(context.Background(), dir, base, run("rev-parse", "HEAD"), true)
	if err != nil || len(paths) != 1 || paths[0].Change != "modified" {
		t.Fatalf("recreated path: %+v %v", paths, err)
	}
}

func TestReviewKeepsUnmergedPathsConflicted(t *testing.T) {
	dir := setupRepo(t)
	run := func(args ...string) { t.Helper(); runGit(t, dir, args...) }
	write := func(text string) { t.Helper(); writeRepoFile(t, dir, "conflict", text) }
	write("base\n")
	run("add", ".")
	run("commit", "-m", "base")
	run("branch", "other")
	write("ours\n")
	run("commit", "-am", "ours")
	run("switch", "other")
	write("theirs\n")
	run("commit", "-am", "theirs")
	cmd := exec.Command("git", "-C", dir, "merge", "--no-edit", "HEAD@{2}")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("expected conflict: %s", out)
	}
	g := gitpkg.New("git")
	head, err := g.ReviewResolve(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	paths, _, err := g.ReviewFiles(context.Background(), dir, head, head, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if p.Path == "conflict" && p.Change == "conflicted" {
			return
		}
	}
	t.Fatalf("unmerged path not identified: %+v", paths)
}

func TestReviewTracksStagedUnstagedDeletedBinaryAndLiteralPaths(t *testing.T) {
	dir := setupRepo(t)
	ctx := context.Background()
	g := gitpkg.New("git")
	run := func(args ...string) string { t.Helper(); return runGitOutput(t, dir, args...) }
	write := func(p, s string) { t.Helper(); writeRepoFile(t, dir, p, s) }
	for _, p := range []string{"staged", "unstaged", "deleted", ":(glob)*"} {
		write(p, "before\n")
	}
	run("add", ".")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	write("staged", "staged change\n")
	run("add", "staged")
	write("unstaged", "unstaged change\n")
	os.Remove(filepath.Join(dir, "deleted"))
	write("new\nfile", "new\n")
	write("binary", "\x00\xff")
	paths, _, err := g.ReviewFiles(ctx, dir, base, base, true)
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]string{}
	for _, p := range paths {
		changes[p.Path] = p.Change
	}
	if changes["staged"] != "modified" || changes["unstaged"] != "modified" || changes["deleted"] != "deleted" || changes["new\nfile"] != "added" || changes["binary"] != "added" {
		t.Fatalf("changes: %+v", changes)
	}
	source, err := g.ReviewSource(ctx, dir, base, ":(glob)*")
	if err != nil || source.Content != "before\n" {
		t.Fatalf("literal path: %+v %v", source, err)
	}
	run("add", ".")
	run("commit", "-m", "edits")
	head := run("rev-parse", "HEAD")
	source, err = g.ReviewSource(ctx, dir, head, "binary")
	if err != nil || !source.Binary || source.Content != "" {
		t.Fatalf("binary: %+v %v", source, err)
	}
	paths, _, err = g.ReviewFiles(ctx, dir, base, head, false)
	if err != nil || len(paths) != 5 {
		t.Fatalf("committed: %v %v", paths, err)
	}
	root := run("rev-list", "--max-parents=0", "HEAD")
	parent, err := g.ReviewParent(ctx, dir, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = g.ReviewFiles(ctx, dir, parent, root, false); err != nil {
		t.Fatalf("root commit: %v", err)
	}
	source, err = g.ReviewSource(ctx, dir, parent, "not-present")
	if err != nil || source.Exists {
		t.Fatalf("empty tree: %+v %v", source, err)
	}
}
