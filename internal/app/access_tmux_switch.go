package app

import (
	"context"
	"fmt"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

// SwitchWorkItem is state-neutral porcelain. It composes required worktree
// materialization, a TUI agent runtime, and optional terminal attachment.
func (a *App) SwitchWorkItem(ctx context.Context, opts ResolveOptions, attach bool) (CompositionResult, error) {
	m, err := a.resolveWorkspaceEntryItem(ctx, opts)
	if err != nil {
		return CompositionResult{}, err
	}
	existing, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil {
		return CompositionResult{}, err
	}
	ownership := a.primaryAgentService().ObserveOwnership(existing)
	if ownership.ProcessAlive {
		if ownership.Mode != "tui" {
			return CompositionResult{}, fmt.Errorf("agent runtime %s currently owns the conversation in %s mode; run `wi agent runtime stop --item %s`, wait for it to exit, then switch to the native TUI", existing.ID, existing.Mode, m.ID)
		}
		if m.RootPiSession == nil {
			return CompositionResult{}, fmt.Errorf("active agent runtime %s has no durable root conversation", existing.ID)
		}
		terminal, err := a.enterExistingTerminal(ctx, ResolveOptions{Selector: m.ID, CWD: opts.CWD, Env: opts.Env}, attach)
		if err != nil {
			return CompositionResult{}, err
		}
		return CompositionResult{WorkItemID: m.ID, Checkout: m.Checkout, Terminal: &terminal, PiSession: m.RootPiSession, AgentRuntime: existing, Manifest: m, Warnings: append([]string(nil), terminal.Warnings...)}, nil
	}
	if err := a.requireTUIRuntimeAvailable(m); err != nil {
		return CompositionResult{}, err
	}
	runtime, err := a.EnsureAgentRuntime(ctx, ResolveOptions{Selector: m.ID, CWD: opts.CWD, Env: opts.Env}, agent.ModeTUI)
	if err != nil {
		return CompositionResult{}, err
	}
	manifest, err := a.Store.LoadManifest(m.ID)
	if err != nil {
		return CompositionResult{}, err
	}
	workspace := CompositionResult{
		WorkItemID: m.ID, Checkout: manifest.Checkout, Terminal: runtime.Terminal,
		PiSession: manifest.RootPiSession, PiLaunched: runtime.Created,
		AgentRuntime: &runtime.Runtime, Manifest: manifest, Warnings: runtime.Warnings,
	}
	if attach {
		terminal, err := a.EnterTerminal(ctx, ResolveOptions{Selector: manifest.ID, CWD: opts.CWD, Env: opts.Env}, true)
		if err != nil {
			return CompositionResult{}, err
		}
		workspace.Terminal = &terminal
		workspace.Warnings = append(workspace.Warnings, terminal.Warnings...)
	}
	return workspace, nil
}

func (a *App) requireTUIRuntimeAvailable(m model.Manifest) error {
	runtime, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil {
		return err
	}
	ownership := a.primaryAgentService().ObserveOwnership(runtime)
	if ownership.ProcessAlive && ownership.Mode != "tui" {
		return fmt.Errorf("agent runtime %s currently owns the conversation in %s mode; run `wi agent runtime stop --item %s`, wait for it to exit, then switch to the native TUI", runtime.ID, runtime.Mode, m.ID)
	}
	return nil
}

func (a *App) resolveWorkspaceEntryItem(ctx context.Context, opts ResolveOptions) (model.Manifest, error) {
	return a.tmuxPorcelainService().ValidateSwitchTarget(ctx, opts)
}
