package main

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// Build contextual refs only inside the isolated fixture, then restore them
// before its normal commit/merge flow. No remote is contacted.
func (f *binaryFixture) seedReviewGraph(checkout string) func() {
	f.t.Helper()
	git := func(dir string, args ...string) string {
		f.t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Graph smoke", "-c", "user.email=graph@example.invalid", "-C", dir}, args...)...)
		cmd.Env = f.env
		out, err := cmd.CombinedOutput()
		if err != nil {
			f.t.Fatalf("graph git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	root := git(checkout, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(root) {
		root = filepath.Join(checkout, root)
	}
	branch := git(root, "symbolic-ref", "--short", "HEAD")
	base := git(root, "rev-parse", "refs/heads/"+branch)
	tree := git(root, "rev-parse", base+"^{tree}")
	commit := func(subject string) string { return git(root, "commit-tree", tree, "-p", base, "-m", subject) }
	local, remote, other := commit("Local graph context"), commit("Remote graph context"), commit("Unrelated secret branch")
	git(root, "update-ref", "refs/heads/"+branch, local)
	git(root, "update-ref", "refs/remotes/review-origin/main", remote)
	git(root, "update-ref", "refs/heads/unrelated-graph", other)
	git(root, "remote", "add", "review-origin", "https://example.invalid/not-contacted.git")
	git(root, "config", "branch."+branch+".remote", "review-origin")
	git(root, "config", "branch."+branch+".merge", "refs/heads/main")
	return func() {
		git(root, "update-ref", "refs/heads/"+branch, base)
		git(root, "config", "--unset", "branch."+branch+".remote")
		git(root, "config", "--unset", "branch."+branch+".merge")
		git(root, "remote", "remove", "review-origin")
		git(root, "update-ref", "-d", "refs/heads/unrelated-graph")
	}
}
