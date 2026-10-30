package app

import (
	"context"
	"errors"
	"testing"

	"github.com/regb/workitem/internal/model"
)

type trackingGit struct {
	Git
	upstream    *model.RepositoryUpstream
	left, right string
}

func (g *trackingGit) BranchUpstream(context.Context, string, string) (*model.RepositoryUpstream, error) {
	return g.upstream, nil
}

func (g *trackingGit) AheadBehind(_ context.Context, _ string, left, right string) (int, int, error) {
	g.left, g.right = left, right
	if left == "refs/heads/gone" {
		return 0, 0, errors.New("unknown revision")
	}
	return 2, 5, nil
}

func TestShowComparesTheBranchWithItsUpstream(t *testing.T) {
	m := model.Manifest{Repository: model.Repository{ID: "repo", Name: "demo", IntegrationBranch: "main", RootAtCreation: "/store.git"}}
	m.Checkout.Specification.Branch = "wi/topic"
	upstream, _ := model.ParseRepositoryUpstream("https://example.com/demo.git", "main")
	git := &trackingGit{upstream: upstream}
	view := (&App{Git: git}).showRepository(context.Background(), m)
	if view.Name != "demo" || view.Upstream != upstream || view.Base != "origin/main" || *view.Ahead != 2 || *view.Behind != 5 {
		t.Fatalf("view = %+v", view)
	}
	if git.left != "refs/heads/wi/topic" || git.right != "refs/remotes/origin/main" {
		t.Fatalf("compared %s with %s", git.left, git.right)
	}

	// Without an upstream the integration branch is the base.
	git.upstream = nil
	if view := (&App{Git: git}).showRepository(context.Background(), m); view.Base != "main" || git.right != "refs/heads/main" {
		t.Fatalf("local-only view = %+v, base %s", view, git.right)
	}

	// A missing branch leaves the counts out rather than failing show.
	m.Checkout.Specification.Branch = "gone"
	if view := (&App{Git: git}).showRepository(context.Background(), m); view.Ahead != nil || view.Base != "" || view.Name != "demo" {
		t.Fatalf("missing branch view = %+v", view)
	}
}
