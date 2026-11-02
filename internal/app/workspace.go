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
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return CheckoutCreateResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	return a.workspaceService().CheckoutCreate(ctx, opts)
}

func (a *App) CheckoutRemove(ctx context.Context, opts ResolveOptions) (CheckoutRemoveResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return CheckoutRemoveResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	return a.workspaceService().CheckoutRemove(ctx, opts)
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
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return CompositionResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	res, err := a.workspaceService().Ensure(ctx, opts)
	if err != nil {
		return CompositionResult{}, err
	}
	return CompositionResult{WorkItemID: res.WorkItemID, Checkout: res.Manifest.Checkout, Manifest: res.Manifest, Warnings: res.Warnings}, nil
}

func (a *App) ensureWorkspaceCheckout(ctx context.Context, opts ResolveOptions, m model.Manifest, create bool) (model.Manifest, []string, error) {
	return a.workspaceService().EnsureCheckout(ctx, opts, m, create)
}

func (a *App) ReleaseWorkItemWorkspace(ctx context.Context, opts ResolveOptions) (WorkspaceReleaseResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return WorkspaceReleaseResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	removed, err := a.removeAttachedCopies(ctx, m)
	if err != nil {
		return WorkspaceReleaseResult{Warnings: removed}, err
	}
	result, err := a.workspaceService().Release(ctx, opts)
	result.Warnings = append(removed, result.Warnings...)
	return result, err
}

// ensureCheckoutReleasable removes unchanged copies of the item's
// attachments, then checks that nothing uncommitted is left. It returns what
// it removed.
func (a *App) ensureCheckoutReleasable(ctx context.Context, m model.Manifest) ([]string, error) {
	removed, err := a.removeAttachedCopies(ctx, m)
	if err != nil {
		return removed, err
	}
	return removed, a.workspaceService().EnsureReleasable(ctx, m)
}

func (a *App) releaseCheckout(ctx context.Context, m model.Manifest) (checkoutRelease, error) {
	return a.workspaceService().ReleaseCheckout(ctx, m)
}

func expectedCheckoutBranch(m model.Manifest) string {
	return workspacecore.ExpectedBranch(m)
}
