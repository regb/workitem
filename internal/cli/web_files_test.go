package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestMentionFilesRankWorktreePathsWithFzf(t *testing.T) {
	if _, err := exec.LookPath("fzf"); err != nil {
		t.Skip("fzf is not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	for name, body := range map[string]string{"docs/readme.md": "docs", "internal/web/server.go": "package web", ".gitignore": "build/\n", "build/out.bin": "binary", "notes.txt": "untracked"} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "-C", dir, "add", "docs", "internal", ".gitignore").CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	ctx := context.Background()
	found, err := findFiles(ctx, dir, "readme")
	if err != nil || len(found) == 0 || found[0] != "docs/readme.md" {
		t.Fatalf("readme: %v %v", found, err)
	}
	if found, _ := findFiles(ctx, dir, "intweb"); !slices.Contains(found, "internal/web/") || !slices.Contains(found, "internal/web/server.go") {
		t.Fatalf("folders and fuzzy match: %v", found)
	}
	all, err := findFiles(ctx, dir, "")
	if err != nil || slices.Contains(all, "build/out.bin") || !slices.Contains(all, "notes.txt") {
		t.Fatalf("ignored or untracked files: %v %v", all, err)
	}
	// The shallowest paths come first without a query.
	if pathDepth(all[0]) != 0 {
		t.Fatalf("empty query order: %v", all)
	}
	if none, err := findFiles(ctx, dir, "zzzzqqq"); err != nil || len(none) != 0 {
		t.Fatalf("no match: %v %v", none, err)
	}
	// New work searches the committed tree of the repository instead.
	if out, err := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "files").CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	committed, err := findCommitFiles(ctx, filepath.Join(dir, ".git"), "HEAD", "servgo")
	if err != nil || len(committed) == 0 || committed[0] != "internal/web/server.go" || slices.Contains(committed, "notes.txt") {
		t.Fatalf("commit files: %v %v", committed, err)
	}
}
