package app

import (
	"context"
	"fmt"

	workspacecore "github.com/regb/workitem/internal/app/core/workspace"
	"github.com/regb/workitem/internal/model"
)

type CheckoutCreateResult = workspacecore.CheckoutCreateResult
type CheckoutRemoveResult = workspacecore.CheckoutRemoveResult
type WorkspaceGitStatus = workspacecore.GitStatus

func (a *App) CheckoutCreate(ctx context.Context, opts ResolveOptions) (CheckoutCreateResult, error) {
	return a.workspaceService().CheckoutCreate(ctx, opts)
}
func (a *App) CheckoutRemove(ctx context.Context, opts ResolveOptions, force bool) (CheckoutRemoveResult, error) {
	return a.workspaceService().CheckoutRemove(ctx, opts, force)
}

type CheckoutStatusResult = workspacecore.CheckoutStatusResult
type WorkspaceStatusResult = workspacecore.StatusResult
type WorkspaceReleaseResult = workspacecore.ReleaseResult
type RelocateRepositoryResult = workspacecore.RelocateRepositoryResult
type checkoutRelease = workspacecore.CheckoutRelease

func expectedCheckoutBranch(m model.Manifest) string { return workspacecore.ExpectedBranch(m) }
func (a *App) CheckoutStatus(ctx context.Context, opts ResolveOptions) (CheckoutStatusResult, error) {
	return a.workspaceService().CheckoutStatus(ctx, opts)
}
func (a *App) WorkspaceStatus(ctx context.Context, opts ResolveOptions) (WorkspaceStatusResult, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return WorkspaceStatusResult{}, err
	}
	if m.Checkout.Specification.Kind != model.WorkspaceKindExecutor {
		return a.workspaceService().Status(ctx, opts)
	}
	result := WorkspaceStatusResult{WorkItemID: m.ID, State: m.State, Checkout: m.Checkout, Manifest: m, Warnings: []string{}}
	result.Git.ExpectedBranch = m.Checkout.Specification.Branch
	if m.Checkout.Claim == nil {
		return result, nil
	}
	if m.Checkout.Claim.Executor == nil || !m.Checkout.Claim.Executor.Ready {
		result.Warnings = append(result.Warnings, "executor materialization is incomplete")
		return result, nil
	}
	c, err := a.executorClient(m)
	if err != nil {
		return result, err
	}
	s, err := c.Status(ctx)
	if err != nil {
		return result, err
	}
	result.Git.CurrentHead = s.Head
	result.Git.CurrentBranch = s.Branch
	result.Git.Dirty = s.Dirty
	result.Git.BranchMatches = s.Branch == result.Git.ExpectedBranch
	result.Git.BranchMismatch = !result.Git.BranchMatches
	return result, nil
}
func (a *App) RelocateWorkItemRepository(ctx context.Context, opts ResolveOptions, path string) (RelocateRepositoryResult, error) {
	return a.workspaceService().RelocateRepository(ctx, opts, path)
}
func (a *App) EnsureWorkItemWorkspace(ctx context.Context, opts ResolveOptions) (CompositionResult, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return CompositionResult{}, err
	}
	if m.Checkout.Specification.Kind == model.WorkspaceKindExecutor {
		m, warnings, err := a.ensureExecutorWorkspace(ctx, m)
		return CompositionResult{WorkItemID: m.ID, Checkout: m.Checkout, Manifest: m, Warnings: warnings}, err
	}
	res, err := a.workspaceService().Ensure(ctx, opts)
	return CompositionResult{WorkItemID: res.Manifest.ID, Checkout: res.Manifest.Checkout, Manifest: res.Manifest, Warnings: res.Warnings}, err
}
func (a *App) ensureWorkspaceCheckout(ctx context.Context, opts ResolveOptions, m model.Manifest, create bool) (model.Manifest, []string, error) {
	if m.Checkout.Specification.Kind == model.WorkspaceKindExecutor {
		if !create && m.Checkout.Claim == nil {
			return m, nil, nil
		}
		return a.ensureExecutorWorkspace(ctx, m)
	}
	return a.workspaceService().EnsureCheckout(ctx, opts, m, create)
}
func (a *App) ReleaseWorkItemWorkspace(ctx context.Context, opts ResolveOptions, force bool) (WorkspaceReleaseResult, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return WorkspaceReleaseResult{}, err
	}
	if m.Checkout.Specification.Kind != model.WorkspaceKindExecutor {
		return a.workspaceService().Release(ctx, opts, force)
	}
	result := WorkspaceReleaseResult{WorkItemID: m.ID, Manifest: m}
	if m.Checkout.Claim == nil {
		return result, nil
	}
	if runtime, err := a.Store.LoadAgentRuntime(m.ID); err != nil {
		return result, err
	} else if a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		return result, fmt.Errorf("stop the agent runtime before releasing its executor")
	}
	m, err = a.releaseExecutor(ctx, m, force)
	result.Manifest = m
	result.Changed = err == nil
	if force && err == nil {
		result.Warnings = []string{"forced executor destruction discarded any uncommitted files"}
	}
	return result, err
}
func (a *App) ensureCheckoutReleasable(ctx context.Context, m model.Manifest) error {
	if m.Checkout.Specification.Kind != model.WorkspaceKindExecutor {
		return a.workspaceService().EnsureReleasable(ctx, m)
	}
	if m.Checkout.Claim == nil || m.Checkout.Claim.Executor == nil || !m.Checkout.Claim.Executor.Ready {
		return nil
	}
	c, err := a.executorClient(m)
	if err != nil {
		return err
	}
	s, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if s.Branch != m.Checkout.Specification.Branch {
		return fmt.Errorf("executor branch differs from item branch")
	}
	if s.Dirty {
		return fmt.Errorf("executor has uncommitted changes")
	}
	return nil
}
func (a *App) releaseCheckout(ctx context.Context, m model.Manifest, force bool) (checkoutRelease, error) {
	if m.Checkout.Specification.Kind != model.WorkspaceKindExecutor {
		return a.workspaceService().ReleaseCheckout(ctx, m, force)
	}
	m, err := a.releaseExecutor(ctx, m, force)
	return checkoutRelease{Manifest: m, Slot: true}, err
}
