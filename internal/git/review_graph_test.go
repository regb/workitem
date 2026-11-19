package git_test

import (
	"context"
	"fmt"
	"testing"

	gitpkg "github.com/regb/workitem/internal/git"
)

func configureGraphUpstream(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "remote", "add", "origin", "https://example.invalid/never-fetch.git")
	runGit(t, dir, "config", "branch.main.remote", "origin")
	runGit(t, dir, "config", "branch.main.merge", "refs/heads/main")
}

func TestReviewGraphShowsOnlySelectedTipsAndTheirAncestry(t *testing.T) {
	dir := setupRepo(t)
	runGit(t, dir, "branch", "-M", "main")
	base := runGitOutput(t, dir, "rev-parse", "HEAD")
	runGit(t, dir, "branch", "wi/task")
	runGit(t, dir, "branch", "unrelated")
	runGit(t, dir, "commit", "--allow-empty", "-m", "local integration")
	local := runGitOutput(t, dir, "rev-parse", "HEAD")
	runGit(t, dir, "switch", "-c", "remote-seed", base)
	runGit(t, dir, "commit", "--allow-empty", "-m", "remote integration")
	remote := runGitOutput(t, dir, "rev-parse", "HEAD")
	runGit(t, dir, "update-ref", "refs/remotes/origin/main", remote)
	configureGraphUpstream(t, dir)
	runGit(t, dir, "switch", "wi/task")
	runGit(t, dir, "commit", "--allow-empty", "-m", "item work")
	runGit(t, dir, "merge", "--no-ff", "main", "-m", "merge integration")
	head := runGitOutput(t, dir, "rev-parse", "HEAD")
	runGit(t, dir, "switch", "unrelated")
	runGit(t, dir, "commit", "--allow-empty", "-m", "must not be shown")
	other := runGitOutput(t, dir, "rev-parse", "HEAD")
	g := gitpkg.New("git")
	graph, err := g.ReviewGraph(context.Background(), dir, "wi/task", "main")
	if err != nil {
		t.Fatal(err)
	}
	if graph.Truncated || len(graph.Commits) != 5 || len(graph.Branches) != 3 || len(graph.Warnings) != 0 {
		t.Fatalf("graph: %+v", graph)
	}
	want := map[string]string{"item": head, "integration": local, "remote": remote}
	for _, branch := range graph.Branches {
		if branch.SHA != want[branch.Kind] {
			t.Fatalf("unexpected branch: %+v", branch)
		}
		delete(want, branch.Kind)
	}
	positions := map[string]int{}
	for i, commit := range graph.Commits {
		if commit.SHA == other {
			t.Fatal("unrelated branch leaked into graph")
		}
		if _, exists := positions[commit.SHA]; exists {
			t.Fatal("shared commit duplicated")
		}
		positions[commit.SHA] = i
		if commit.SHA == head && len(commit.Parents) != 2 {
			t.Fatal("merge parents lost", commit)
		}
	}
	for i, commit := range graph.Commits {
		for _, parent := range commit.Parents {
			if at, ok := positions[parent]; ok && at <= i {
				t.Fatal("not in topological order", graph.Commits)
			}
		}
	}
	if _, ok := positions[base]; !ok {
		t.Fatal("shared ancestry missing")
	}
}

func TestReviewGraphHandlesLocalOnlyAndMissingCachedRemote(t *testing.T) {
	dir := setupRepo(t)
	runGit(t, dir, "branch", "-M", "main")
	runGit(t, dir, "branch", "wi/task")
	g := gitpkg.New("git")
	graph, err := g.ReviewGraph(context.Background(), dir, "wi/task", "main")
	if err != nil || len(graph.Commits) != 1 || len(graph.Branches) != 2 || len(graph.Warnings) != 1 {
		t.Fatalf("local graph: %+v %v", graph, err)
	}
	configureGraphUpstream(t, dir)
	graph, err = g.ReviewGraph(context.Background(), dir, "wi/task", "main")
	if err != nil || len(graph.Commits) != 1 || len(graph.Branches) != 3 || graph.Branches[2].SHA != "" || len(graph.Warnings) != 1 {
		t.Fatalf("uncached graph: %+v %v", graph, err)
	}
}

func TestReviewGraphBoundsHistoryAndKeepsRealBoundaryParents(t *testing.T) {
	dir := setupRepo(t)
	runGit(t, dir, "branch", "-M", "main")
	head := runGitOutput(t, dir, "rev-parse", "HEAD")
	tree := runGitOutput(t, dir, "rev-parse", "HEAD^{tree}")
	for i := 0; i < gitpkg.ReviewGraphLimit+5; i++ {
		head = runGitOutput(t, dir, "commit-tree", tree, "-p", head, "-m", fmt.Sprintf("commit %d", i))
	}
	runGit(t, dir, "update-ref", "refs/heads/wi/task", head)
	graph, err := gitpkg.New("git").ReviewGraph(context.Background(), dir, "wi/task", "main")
	if err != nil || !graph.Truncated || len(graph.Commits) != gitpkg.ReviewGraphLimit {
		t.Fatalf("bounded graph: %+v %v", graph, err)
	}
	if len(graph.Commits[len(graph.Commits)-1].Parents) != 1 {
		t.Fatal("boundary parent was erased")
	}
}
