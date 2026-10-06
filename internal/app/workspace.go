package app

import (
	"context"
	"encoding/json"
	"fmt"

	workspacecore "github.com/regb/workitem/internal/app/core/workspace"
	"github.com/regb/workitem/internal/model"
)

type CheckoutCreateResult = workspacecore.CheckoutCreateResult
type CheckoutRemoveResult = workspacecore.CheckoutRemoveResult
type CheckoutStatusResult = workspacecore.CheckoutStatusResult
type WorkspaceGitStatus = workspacecore.GitStatus
type WorkspaceStatusResult = workspacecore.StatusResult
type RelocateRepositoryResult = workspacecore.RelocateRepositoryResult
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
	manifest, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return WorkspaceStatusResult{}, err
	}
	if manifest.Checkout.Specification.Executor != "" && manifest.Checkout.Specification.Executor != model.ExecutorLocal {
		client, err := a.executorClient(manifest.Checkout.Specification.Executor)
		if err != nil {
			return WorkspaceStatusResult{}, err
		}
		remote, err := client.WorkspaceStatus(ctx, manifest.ID)
		if err != nil {
			return WorkspaceStatusResult{}, err
		}
		var result WorkspaceStatusResult
		if err := json.Unmarshal(remote.StatusJSON, &result); err != nil {
			return WorkspaceStatusResult{}, err
		}
		result.Checkout, result.Manifest = manifest.Checkout, manifest
		return result, nil
	}
	return a.workspaceService().Status(ctx, opts)
}

func (a *App) RelocateWorkItemRepository(ctx context.Context, opts ResolveOptions, path string) (RelocateRepositoryResult, error) {
	return a.workspaceService().RelocateRepository(ctx, opts, path)
}

// EnsureWorkItemWorkspace materializes only the repository worktree.
func (a *App) EnsureWorkItemWorkspace(ctx context.Context, opts ResolveOptions) (CompositionResult, error) {
	manifest, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return CompositionResult{}, err
	}
	if manifest.Checkout.Specification.Executor != "" && manifest.Checkout.Specification.Executor != model.ExecutorLocal {
		manifest, warnings, err := a.ensureRemoteWorkspace(ctx, manifest)
		return CompositionResult{WorkItemID: manifest.ID, Checkout: manifest.Checkout, Manifest: manifest, Warnings: warnings}, err
	}
	res, err := a.workspaceService().Ensure(ctx, opts)
	if err != nil {
		return CompositionResult{}, err
	}
	return CompositionResult{WorkItemID: res.WorkItemID, Checkout: res.Manifest.Checkout, Manifest: res.Manifest, Warnings: res.Warnings}, nil
}

func (a *App) ensureWorkspaceCheckout(ctx context.Context, opts ResolveOptions, m model.Manifest, create bool) (model.Manifest, []string, error) {
	if m.Checkout.Specification.Executor != "" && m.Checkout.Specification.Executor != model.ExecutorLocal {
		if !create && !m.Checkout.Present() {
			return m, nil, nil
		}
		return a.ensureRemoteWorkspace(ctx, m)
	}
	return a.workspaceService().EnsureCheckout(ctx, opts, m, create)
}

func (a *App) ReleaseWorkItemWorkspace(ctx context.Context, opts ResolveOptions, force bool) (WorkspaceReleaseResult, error) {
	manifest, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return WorkspaceReleaseResult{}, err
	}
	if manifest.Checkout.Specification.Executor != "" && manifest.Checkout.Specification.Executor != model.ExecutorLocal {
		hadCheckout := manifest.Checkout.Present()
		if !hadCheckout {
			return WorkspaceReleaseResult{WorkItemID: manifest.ID, Manifest: manifest}, nil
		}
		if runtime, runtimeErr := a.Store.LoadAgentRuntime(manifest.ID); runtimeErr != nil {
			return WorkspaceReleaseResult{}, runtimeErr
		} else if a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
			return WorkspaceReleaseResult{}, fmt.Errorf("agent runtime %s is still active; run `wi agent runtime stop --item %s`, wait for it to exit, then release the workspace", runtime.ID, manifest.ID)
		}
		if a.Tmux != nil {
			exists, inspectErr := a.Tmux.HasSession(ctx, manifest.TerminalSessionName())
			if inspectErr != nil {
				return WorkspaceReleaseResult{}, fmt.Errorf("could not inspect optional terminal before releasing workspace: %w", inspectErr)
			}
			if exists {
				return WorkspaceReleaseResult{}, fmt.Errorf("terminal session %s is still present; run `wi terminal close --item %s` before releasing the workspace", manifest.TerminalSessionName(), manifest.ID)
			}
		}
		if err := a.ensureCheckoutReleasable(ctx, manifest); err != nil {
			return WorkspaceReleaseResult{}, err
		}
		client, err := a.executorClient(manifest.Checkout.Specification.Executor)
		if err != nil {
			return WorkspaceReleaseResult{}, err
		}
		remote, err := client.ReleaseWorkspace(ctx, manifest, force)
		if err != nil {
			return WorkspaceReleaseResult{}, err
		}
		if err := client.ImportWorkspaceBundle(ctx, manifest, remote.GitBundle); err != nil {
			return WorkspaceReleaseResult{}, err
		}
		previousClaim := *manifest.Checkout.Claim
		manifest.Checkout.ClearClaim()
		manifest.UpdatedAt = a.now()
		if err := a.Store.SaveManifest(ctx, manifest); err != nil {
			return WorkspaceReleaseResult{}, err
		}
		_ = a.Store.AppendEvent(ctx, manifest.ID, model.NewEvent(a.now(), "workspace.released", "user", map[string]any{"claim_id": previousClaim.ID, "claim_generation": previousClaim.Generation, "executor": previousClaim.Executor, "forced": force}))
		return WorkspaceReleaseResult{WorkItemID: manifest.ID, Changed: true, Manifest: manifest, Warnings: []string{}}, nil
	}
	return a.workspaceService().Release(ctx, opts, force)
}

func (a *App) ensureCheckoutReleasable(ctx context.Context, m model.Manifest) error {
	if m.Checkout.Specification.Executor != "" && m.Checkout.Specification.Executor != model.ExecutorLocal {
		status, err := a.WorkspaceStatus(ctx, ResolveOptions{Selector: m.ID})
		if err != nil {
			return err
		}
		if status.Git.BranchMismatch {
			return fmt.Errorf("remote checkout branch differs from expected branch")
		}
		if status.Git.Dirty {
			return fmt.Errorf("remote checkout has uncommitted changes")
		}
		return nil
	}
	return a.workspaceService().EnsureReleasable(ctx, m)
}

func (a *App) releaseCheckout(ctx context.Context, m model.Manifest, force bool) (checkoutRelease, error) {
	if m.Checkout.Specification.Executor != "" && m.Checkout.Specification.Executor != model.ExecutorLocal {
		client, err := a.executorClient(m.Checkout.Specification.Executor)
		if err != nil {
			return checkoutRelease{}, err
		}
		remote, err := client.ReleaseWorkspace(ctx, m, force)
		if err != nil {
			return checkoutRelease{}, err
		}
		if err := client.ImportWorkspaceBundle(ctx, m, remote.GitBundle); err != nil {
			return checkoutRelease{}, err
		}
		m.Checkout.ClearClaim()
		return checkoutRelease{Manifest: m, Slot: true}, nil
	}
	return a.workspaceService().ReleaseCheckout(ctx, m, force)
}

func expectedCheckoutBranch(m model.Manifest) string {
	return workspacecore.ExpectedBranch(m)
}
