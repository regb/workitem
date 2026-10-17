package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	workspacecore "github.com/regb/workitem/internal/app/core/workspace"
	"github.com/regb/workitem/internal/model"
)

func (a *App) Shelve(ctx context.Context, opts ResolveOptions, force bool) (StateTransitionResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return StateTransitionResult{}, err
	}
	defer operation.Release()
	if m.State == model.StateClosed {
		return StateTransitionResult{}, fmt.Errorf("work item %s is closed; run `wi state set backlog --item %s` before shelving it", m.ID, m.ID)
	}
	prevState := m.State
	if prevState == model.StateBacklog {
		cap := a.DeepWorkCapacity()
		return StateTransitionResult{WorkItemID: m.ID, PreviousState: prevState, State: m.State, Changed: false, Manifest: m, DeepWork: m.DeepWork, Capacity: &cap}, nil
	}
	if prevState != model.StateActive {
		return StateTransitionResult{}, fmt.Errorf("invalid state transition %s -> %s", prevState, model.StateBacklog)
	}
	m, workspaceWarnings, workspaceChanged, err := a.closeWorkspace(ctx, m, force, opts.Env)
	if err != nil {
		return StateTransitionResult{}, err
	}
	now := a.now()
	m.State = model.StateBacklog
	m.StateChangedAt = now
	m.UpdatedAt = now
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		return StateTransitionResult{}, err
	}
	data := map[string]any{"previous_state": prevState, "new_state": m.State, "workspace_closed": workspaceChanged, "forced": force}
	_ = a.Store.AppendEvent(ctx, m.ID, model.NewEvent(now, "work_item.shelved", "user", data))
	cap := a.DeepWorkCapacity()
	return StateTransitionResult{WorkItemID: m.ID, PreviousState: prevState, State: m.State, Changed: true, Manifest: m, DeepWork: m.DeepWork, Capacity: &cap, Warnings: workspaceWarnings}, nil
}

// Close stops the runtime, releases the workspace, and records the item as
// closed with a resolution. Whether the work was completed is independent of
// the item being closed.
func (a *App) Close(ctx context.Context, opts ResolveOptions, resolution string, force bool) (StateTransitionResult, error) {
	if resolution == "" {
		resolution = model.ResolutionCompleted
	}
	if !model.ValidResolution(resolution) {
		return StateTransitionResult{}, fmt.Errorf("invalid resolution %q; expected completed, abandoned, or superseded", resolution)
	}
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return StateTransitionResult{}, err
	}
	defer operation.Release()
	prevState := m.State
	if m.State == model.StateClosed {
		return StateTransitionResult{WorkItemID: m.ID, PreviousState: prevState, State: m.State, Changed: false, Manifest: m}, nil
	}
	warnings, err := a.prepareArchive(ctx, m, opts, force)
	if err != nil {
		return StateTransitionResult{WorkItemID: m.ID, PreviousState: prevState, State: m.State, Manifest: m, Warnings: warnings}, err
	}
	return a.finishArchive(ctx, m, prevState, resolution, opts.Env, force, warnings)
}

func (a *App) prepareArchive(ctx context.Context, m model.Manifest, opts ResolveOptions, force bool) ([]string, error) {
	// Validate worktree safety before changing any live runtime state.
	if err := a.ensureCheckoutReleasable(ctx, m); err != nil {
		return nil, err
	}
	return a.stopRuntimeForArchive(ctx, m, opts, force)
}

func (a *App) finishArchive(ctx context.Context, m model.Manifest, prevState, resolution string, env map[string]string, force bool, warnings []string, deferTerminalClose ...bool) (StateTransitionResult, error) {
	previousClaim := m.Checkout.Claim
	workspaceChanged := false
	deferredSession := ""
	var workspaceWarnings []string
	var err error
	if len(deferTerminalClose) > 0 && deferTerminalClose[0] {
		m, workspaceWarnings, workspaceChanged, deferredSession, err = a.releaseWorkspaceBeforeDeferredTerminalClose(ctx, m)
	} else {
		m, workspaceWarnings, workspaceChanged, err = a.closeWorkspace(ctx, m, force, env)
	}
	if err != nil {
		return StateTransitionResult{WorkItemID: m.ID, PreviousState: prevState, State: m.State, Manifest: m, Warnings: append(warnings, workspaceWarnings...)}, fmt.Errorf("close not committed; external resources may already have changed: %w", err)
	}
	retained := m
	now := a.now()
	previousSlug := m.Slug
	// Commit workspace release separately from the typed lifecycle transition.
	// The typed close command accepts lifecycle, resolution, slug, and blocker
	// changes, not checkout edits. Retain a successful release if closing fails.
	var saveErr error
	if previousClaim != nil && m.Checkout.Claim == nil {
		m.UpdatedAt = now
		saveErr = a.Store.SaveManifest(ctx, m)
		if saveErr == nil {
			_ = a.Store.AppendEvent(ctx, m.ID, model.NewEvent(now, "workspace.released", "user", map[string]any{"claim_id": previousClaim.ID, "claim_generation": previousClaim.Generation, "executor": previousClaim.Executor, "state": m.State}))
		}
	}
	m.State = model.StateClosed
	m.Slug = ""
	m.Resolution = resolution
	m.Blockers = nil
	m.StateChangedAt = now
	m.UpdatedAt = now
	if saveErr == nil {
		saveErr = a.Store.SaveManifest(ctx, m)
	}
	if saveErr != nil {
		warnings = append(warnings, workspaceWarnings...)
		// Saving can fail before commit or after a lost acknowledgement. Report
		// canonical state, not the locally released claim or assumed lifecycle.
		if current, inspectErr := a.Store.LoadManifest(m.ID); inspectErr == nil {
			retained = current
		} else {
			warnings = append(warnings, "could not refresh retained close state: "+inspectErr.Error())
		}
		return StateTransitionResult{WorkItemID: m.ID, PreviousState: prevState, State: retained.State, Changed: retained.State != prevState, Manifest: retained, Warnings: warnings}, fmt.Errorf("workspace cleanup completed, but closed state could not be saved: %w", saveErr)
	}
	_ = a.Store.AppendEvent(ctx, m.ID, model.NewEvent(now, "work_item.closed", "user", map[string]any{"previous_state": prevState, "new_state": m.State, "resolution": resolution, "previous_slug": previousSlug, "workspace_closed": workspaceChanged, "forced": force}))
	warnings = append(warnings, workspaceWarnings...)
	if deferredSession != "" {
		if err := a.Tmux.KillSessionAsync(ctx, deferredSession); err != nil {
			warnings = append(warnings, "item is closed, but the previous terminal could not be scheduled for closure: "+err.Error())
		} else {
			warnings = append(warnings, "scheduled previous terminal session for closure: "+deferredSession)
			_ = a.Store.AppendEvent(ctx, m.ID, model.NewEvent(a.now(), "terminal.close_scheduled", "wi", map[string]any{"session": deferredSession, "reason": "lifecycle_cleanup"}))
			if err := a.Store.RemoveTerminalRuntime(ctx, m.ID); err != nil {
				warnings = append(warnings, "could not remove terminal-runtime handle cache: "+err.Error())
			}
		}
	}
	return StateTransitionResult{WorkItemID: m.ID, PreviousState: prevState, State: m.State, Changed: true, Manifest: m, Warnings: warnings}, nil
}

// releaseWorkspaceBeforeDeferredTerminalClose is the special finalization path
// used after tmux switch-client. The wi process still runs in the previous
// pane, so killing that session synchronously would kill wi before it can save
// archived state. Only reusable slots are eligible: releasing one updates the
// durable assignment without deleting the process's current directory.
func (a *App) releaseWorkspaceBeforeDeferredTerminalClose(ctx context.Context, m model.Manifest) (model.Manifest, []string, bool, string, error) {
	warnings := []string{}
	if runtime, err := a.Store.LoadAgentRuntime(m.ID); err != nil {
		return m, warnings, false, "", err
	} else if a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		return m, warnings, false, "", fmt.Errorf("agent runtime %s is still active", runtime.ID)
	}
	if err := a.ensureCheckoutReleasable(ctx, m); err != nil {
		return m, warnings, false, "", err
	}
	if m.Checkout.Present() && m.Checkout.Claim != nil && !workspacecore.IsSlotPathName(m.Checkout.Claim.Path) {
		return m, warnings, false, "", fmt.Errorf("cannot defer terminal closure for non-slot worktree %s", m.Checkout.Claim.Path)
	}
	session := strings.TrimSpace(m.TerminalSessionName())
	if a.Tmux != nil && session != "" {
		exists, err := a.Tmux.HasSession(ctx, session)
		if err != nil {
			return m, warnings, false, "", fmt.Errorf("could not inspect previous terminal before archiving: %w", err)
		}
		if !exists {
			session = ""
		}
	}
	hadCheckoutClaim := m.Checkout.Present() && m.Checkout.Claim != nil && m.Checkout.Claim.Path != ""
	released, err := a.releaseCheckout(ctx, m)
	if err != nil {
		return m, warnings, false, "", err
	}
	warnings = append(warnings, released.Warnings...)
	return released.Manifest, warnings, hadCheckoutClaim || session != "", session, nil
}

func (a *App) stopRuntimeForArchive(ctx context.Context, m model.Manifest, opts ResolveOptions, force bool) ([]string, error) {
	runtime, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil {
		return nil, err
	}
	if !a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		return nil, nil
	}
	stopped, err := a.primaryAgentService().Stop(ctx, ResolveOptions{Selector: m.ID, CWD: opts.CWD, Env: opts.Env}, force)
	warnings := append([]string{}, stopped.Warnings...)
	if err != nil {
		return warnings, fmt.Errorf("stop agent runtime before archiving: %w", err)
	}
	if stopped.Changed {
		warnings = append(warnings, "requested agent runtime shutdown")
	}
	timeout := a.ArchiveRuntimeStopTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		if err := ctx.Err(); err != nil {
			return warnings, err
		}
		if !time.Now().Before(deadline) {
			return warnings, fmt.Errorf("agent runtime %s did not exit within %s; archive was not applied", runtime.ID, timeout)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return warnings, ctx.Err()
		case <-timer.C:
		}
	}
	return warnings, nil
}

func (a *App) closeWorkspace(ctx context.Context, m model.Manifest, force bool, env map[string]string) (model.Manifest, []string, bool, error) {
	warnings := []string{}
	changed := false
	tmuxSession := strings.TrimSpace(m.TerminalSessionName())
	runtime, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil {
		return m, warnings, changed, err
	}
	if a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		return m, warnings, changed, fmt.Errorf("agent runtime %s is still active; run `wi agent runtime stop --item %s`, wait for it to exit, then retry", runtime.ID, m.ID)
	}
	terminal, err := a.Store.LoadTerminalRuntime(m.ID)
	if err != nil {
		return m, warnings, changed, err
	}
	if terminal == nil && (runtime == nil || runtime.Mode != "tui") {
		tmuxSession = ""
	}
	if err := a.ensureCheckoutReleasable(ctx, m); err != nil {
		return m, warnings, changed, err
	}

	canReleaseCheckout := true
	if a.Tmux != nil && tmuxSession != "" {
		if inTmux(env) {
			current, err := a.Tmux.CurrentSession(ctx)
			if err != nil {
				if !force {
					return m, warnings, changed, fmt.Errorf("could not determine current tmux session before closing workspace: %w", err)
				}
				warnings = append(warnings, "could not determine current tmux session; left tmux session and checkout running")
				canReleaseCheckout = false
			} else if current == tmuxSession {
				if !force {
					return m, warnings, changed, fmt.Errorf("cannot close current tmux session %s; run this command from outside it or pass --force to leave the workspace open", tmuxSession)
				}
				warnings = append(warnings, "left current terminal and checkout running; exit it, run `wi terminal close`, then `wi workspace release` when clean")
				canReleaseCheckout = false
			}
		}
		if canReleaseCheckout {
			exists, err := a.Tmux.HasSession(ctx, tmuxSession)
			if err != nil {
				if !force {
					return m, warnings, changed, fmt.Errorf("could not inspect tmux session before closing workspace: %w", err)
				}
				warnings = append(warnings, "could not inspect tmux session; left tmux session and checkout running: "+err.Error())
				canReleaseCheckout = false
			} else if exists {
				if err := a.Tmux.KillSession(ctx, tmuxSession); err != nil {
					if !force {
						return m, warnings, changed, fmt.Errorf("could not close tmux session before releasing checkout: %w", err)
					}
					warnings = append(warnings, "could not close tmux session; left checkout assigned: "+err.Error())
					canReleaseCheckout = false
				} else {
					changed = true
					warnings = append(warnings, "closed terminal session "+tmuxSession)
					_ = a.Store.AppendEvent(ctx, m.ID, model.NewEvent(a.now(), "terminal.closed", "wi", map[string]any{"session": tmuxSession, "reason": "lifecycle_cleanup"}))
					if err := a.Store.RemoveTerminalRuntime(ctx, m.ID); err != nil {
						warnings = append(warnings, "could not remove terminal-runtime handle cache: "+err.Error())
					}
				}
			}
		}
	}
	if !canReleaseCheckout {
		return m, warnings, changed, nil
	}

	hadCheckout := m.Checkout.Present() && m.Checkout.Claim != nil && m.Checkout.Claim.Path != ""
	released, err := a.releaseCheckout(ctx, m)
	if err != nil {
		return m, warnings, changed, err
	}
	m = released.Manifest
	warnings = append(warnings, released.Warnings...)
	if hadCheckout {
		changed = true
	}
	if err := a.Store.RemoveTerminalRuntime(ctx, m.ID); err != nil {
		warnings = append(warnings, "could not remove terminal-runtime handle cache: "+err.Error())
	}
	return m, warnings, changed, nil
}
