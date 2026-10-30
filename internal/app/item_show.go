package app

import (
	"context"
	"strings"

	itemcore "github.com/regb/workitem/internal/app/core/item"
	"github.com/regb/workitem/internal/model"
)

type ShowResult struct {
	Manifest    model.Manifest `json:"manifest"`
	Description string         `json:"description"`
	// Repository is where the item's branch stands, observed from the
	// repository's Git store; absent for items outside a registered one.
	Repository *ShowRepository `json:"repository,omitempty"`
}

// ShowRepository names an item's repository and compares its branch with
// the upstream, or with the integration branch when there is none. It reads
// the store's refs without fetching, so the upstream is as of its last fetch.
type ShowRepository struct {
	Name              string                    `json:"name"`
	IntegrationBranch string                    `json:"integration_branch"`
	Upstream          *model.RepositoryUpstream `json:"upstream,omitempty"`
	UpstreamError     string                    `json:"upstream_error,omitempty"`
	// Base is the ref the branch is compared with. Ahead and Behind are
	// absent when either ref is missing, such as a closed item's deleted
	// branch.
	Base   string `json:"base,omitempty"`
	Ahead  *int   `json:"ahead,omitempty"`
	Behind *int   `json:"behind,omitempty"`
}

// branchTracking is the Git observation show needs beyond App.Git.
type branchTracking interface {
	BranchUpstream(ctx context.Context, dir, branch string) (*model.RepositoryUpstream, error)
	AheadBehind(ctx context.Context, dir, left, right string) (int, int, error)
}

// ShowWorkItem returns durable metadata with DESCRIPTION.md hydrated. The
// result shape is shared by text and JSON presentation.
func (a *App) ShowWorkItem(ctx context.Context, opts ResolveOptions) (ShowResult, error) {
	res, err := a.itemService().Show(ctx, opts)
	if err != nil {
		return ShowResult{}, err
	}
	return ShowResult{Manifest: res.Manifest, Description: res.Description, Repository: a.showRepository(ctx, res.Manifest)}, nil
}

func (a *App) showRepository(ctx context.Context, m model.Manifest) *ShowRepository {
	repo := m.Repository
	if repo.ID == "" || repo.Name == "" {
		return nil
	}
	view := &ShowRepository{Name: repo.Name, IntegrationBranch: repo.IntegrationBranch}
	git, ok := a.Git.(branchTracking)
	dir := repo.OperationalRoot()
	if !ok || dir == "" || repo.IntegrationBranch == "" {
		return view
	}
	upstream, err := git.BranchUpstream(ctx, dir, repo.IntegrationBranch)
	if err != nil {
		view.UpstreamError = err.Error()
	}
	view.Upstream = upstream
	base := "refs/heads/" + repo.IntegrationBranch
	if upstream != nil {
		base = upstream.Ref()
	}
	branch := m.Checkout.Specification.Branch
	if branch == "" {
		return view
	}
	if ahead, behind, err := git.AheadBehind(ctx, dir, "refs/heads/"+branch, base); err == nil {
		view.Base = strings.TrimPrefix(strings.TrimPrefix(base, "refs/remotes/"), "refs/heads/")
		view.Ahead, view.Behind = &ahead, &behind
	}
	return view
}

type WorkItemEventsResult = itemcore.EventsResult

func (a *App) WorkItemEvents(ctx context.Context, opts ResolveOptions) (WorkItemEventsResult, error) {
	return a.itemService().Events(ctx, opts)
}
