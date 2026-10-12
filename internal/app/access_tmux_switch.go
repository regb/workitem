package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

// orphanRuntimeStopTimeout bounds how long wi waits for a recycled runtime to
// exit before falling back to a manual recovery hint.
const orphanRuntimeStopTimeout = 5 * time.Second

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
	var recycleWarnings []string
	if ownership.ProcessAlive {
		if ownership.Mode != "tui" {
			return CompositionResult{}, fmt.Errorf("agent runtime %s currently owns the conversation in %s mode; run `wi agent runtime stop --item %s`, wait for it to exit, then switch to the native TUI", existing.ID, existing.Mode, m.ID)
		}
		if m.PrimaryConversation == nil {
			return CompositionResult{}, fmt.Errorf("active agent runtime %s has no durable root conversation", existing.ID)
		}
		if a.tmuxTerminalExists(ctx, m) {
			terminal, err := a.enterExistingTerminal(ctx, ResolveOptions{Selector: m.ID, CWD: opts.CWD, Env: opts.Env}, attach)
			if err != nil {
				return CompositionResult{}, err
			}
			return CompositionResult{WorkItemID: m.ID, Checkout: m.Checkout, Terminal: &terminal, Conversation: m.PrimaryConversation, AgentRuntime: existing, Manifest: m, Warnings: append([]string(nil), terminal.Warnings...)}, nil
		}
		// The runtime process is verified alive but its tmux terminal is gone,
		// typically because the tmux server restarted. The process is
		// unreachable, so recycle it and fall through to start a fresh TUI
		// runtime in a new terminal that resumes the same conversation.
		if err := a.ValidateAgentConfig(ctx, m.Agent); err != nil {
			return CompositionResult{}, err
		}
		recycled, err := a.recycleOrphanedTUIRuntime(ctx, m, existing)
		if err != nil {
			return CompositionResult{}, err
		}
		recycleWarnings = append(recycleWarnings, recycled...)
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
		Conversation: manifest.PrimaryConversation, AgentLaunched: runtime.Created,
		AgentRuntime: &runtime.Runtime, Manifest: manifest, Warnings: append(append([]string(nil), runtime.Warnings...), recycleWarnings...),
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

func (a *App) tmuxTerminalExists(ctx context.Context, m model.Manifest) bool {
	if a.Tmux == nil {
		return false
	}
	exists, err := a.Tmux.HasSession(ctx, m.TerminalSessionName())
	return err == nil && exists
}

// recycleOrphanedTUIRuntime force-stops a verified TUI runtime whose tmux
// terminal has disappeared and waits for it to exit. The conversation is
// durable, so a later EnsureAgentRuntime resumes the same primary conversation in a
// fresh terminal.
func (a *App) recycleOrphanedTUIRuntime(ctx context.Context, m model.Manifest, runtime *model.AgentRuntime) ([]string, error) {
	stopped, err := a.StopAgentRuntime(ctx, ResolveOptions{Selector: m.ID}, true)
	if err != nil {
		return nil, fmt.Errorf("TUI runtime %s is still running but its tmux terminal %s is gone; stop it with `wi agent runtime stop --force --item %s`, wait for it to exit, then switch again: %w", runtime.ID, m.TerminalSessionName(), m.ID, err)
	}
	service := a.primaryAgentService()
	deadline := time.Now().Add(orphanRuntimeStopTimeout)
	for service.ObserveOwnership(runtime).ProcessAlive && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if service.ObserveOwnership(runtime).ProcessAlive {
		return nil, fmt.Errorf("orphaned TUI runtime %s would not exit after its terminal disappeared; run `wi agent runtime stop --force --item %s`, wait for it to exit, then switch again", runtime.ID, m.ID)
	}
	warnings := append([]string(nil), stopped.Warnings...)
	warnings = append(warnings, strings.Join([]string{
		"TUI runtime " + runtime.ID + " was still running but its tmux terminal " + m.TerminalSessionName() + " was gone (tmux server restarted?);",
		"stopped it and started a fresh terminal that resumes the same conversation",
	}, " "))
	return warnings, nil
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
