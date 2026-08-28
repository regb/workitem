package app

import (
	"context"

	workspacecore "github.com/regb/workitem/internal/app/core/workspace"
	"github.com/regb/workitem/internal/model"
)

type CheckoutCreateResult = workspacecore.CheckoutCreateResult
type CheckoutRemoveResult = workspacecore.CheckoutRemoveResult
type CheckoutStatusResult = workspacecore.CheckoutStatusResult
type WorkspaceGitStatus = workspacecore.GitStatus
type WorkspaceStatusResult = workspacecore.StatusResult
type WorkspaceReleaseResult = workspacecore.ReleaseResult
type checkoutRelease = workspacecore.CheckoutRelease

func (a *App) CheckoutCreate(ctx context.Context, opts ResolveOptions) (CheckoutCreateResult, error) {
	return a.workspaceService().CheckoutCreate(ctx, opts)
}

func (a *App) CheckoutRemove(ctx context.Context, opts ResolveOptions, force bool) (CheckoutRemoveResult, error) {
	return a.workspaceService().CheckoutRemove(ctx, opts, force)
}

func (a *App) CheckoutStatus(ctx context.Context, opts ResolveOptions) (CheckoutStatusResult, error) {
	return a.workspaceService().CheckoutStatus(ctx, opts)
}

// WorkspaceStatus inspects only the repository worktree.
func (a *App) WorkspaceStatus(ctx context.Context, opts ResolveOptions) (WorkspaceStatusResult, error) {
	return a.workspaceService().Status(ctx, opts)
}

// EnsureWorkItemWorkspace materializes only the repository worktree.
func (a *App) EnsureWorkItemWorkspace(ctx context.Context, opts ResolveOptions) (CompositionResult, error) {
	res, err := a.workspaceService().Ensure(ctx, opts)
	if err != nil {
		return CompositionResult{}, err
	}
	return CompositionResult{WorkItemID: res.WorkItemID, Checkout: res.Manifest.Checkout, Manifest: res.Manifest, Warnings: res.Warnings}, nil
}

func (a *App) ensureWorkspaceCheckout(ctx context.Context, opts ResolveOptions, m model.Manifest, create bool) (model.Manifest, []string, error) {
	return a.workspaceService().EnsureCheckout(ctx, opts, m, create)
}

func (a *App) ReleaseWorkItemWorkspace(ctx context.Context, opts ResolveOptions, force bool) (WorkspaceReleaseResult, error) {
	return a.workspaceService().Release(ctx, opts, force)
}

func (a *App) ensureCheckoutReleasable(ctx context.Context, m model.Manifest) error {
	return a.workspaceService().EnsureReleasable(ctx, m)
}

func (a *App) releaseCheckout(ctx context.Context, m model.Manifest, force bool) (checkoutRelease, error) {
	return a.workspaceService().ReleaseCheckout(ctx, m, force)
}

func expectedCheckoutBranch(m model.Manifest) string {
	return workspacecore.ExpectedBranch(m)
}
