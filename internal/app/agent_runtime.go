package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/regb/workitem/internal/agent"
	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/model"
)

type EnsureAgentRuntimeResult struct {
	WorkItemID   string             `json:"work_item_id"`
	Created      bool               `json:"created"`
	Runtime      model.AgentRuntime `json:"runtime"`
	Conversation model.Conversation `json:"conversation"`
	Capabilities agent.Capabilities `json:"capabilities"`
	Terminal     *TerminalResult    `json:"terminal,omitempty"`
	Warnings     []string           `json:"warnings"`
}

type AgentRuntimeStatusResult struct {
	WorkItemID   string              `json:"work_item_id"`
	Runtime      *model.AgentRuntime `json:"runtime,omitempty"`
	Online       bool                `json:"online"`
	Capabilities agent.Capabilities  `json:"capabilities"`
	Warnings     []string            `json:"warnings"`
}

type StopAgentRuntimeResult = agentcore.StopResult

func (a *App) EnsureAgentRuntime(ctx context.Context, opts ResolveOptions, mode agent.Mode) (EnsureAgentRuntimeResult, error) {
	return a.ensureAgentRuntime(ctx, opts, mode, false)
}

// EnsureNewAgentRuntime applies queued startup only to a fresh working item.
// The guard is checked under the same operation lock as workspace/runtime creation.
func (a *App) EnsureNewAgentRuntime(ctx context.Context, opts ResolveOptions, mode agent.Mode) (EnsureAgentRuntimeResult, error) {
	return a.ensureAgentRuntime(ctx, opts, mode, true)
}

func (a *App) ensureAgentRuntime(ctx context.Context, opts ResolveOptions, mode agent.Mode, fresh bool) (EnsureAgentRuntimeResult, error) {
	if !mode.Valid() {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("invalid agent runtime mode %q", mode)
	}
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	defer operation.Release()
	if fresh && (m.State != model.StateActive || m.PrimaryConversation != nil) {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("work item %s changed while queued; startup was not applied", m.ID)
	}
	if m.State == model.StateClosed {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("work item %s is closed; set it to backlog before ensuring an agent runtime", m.ID)
	}
	existing, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	if !a.primaryAgentService().ObserveOwnership(existing).ProcessAlive {
		if err := a.validateItemAgent(ctx, m); err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
	}
	m, warnings, err := a.ensureWorkspaceCheckout(ctx, opts, m, true)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	if ownership := a.primaryAgentService().ObserveOwnership(existing); ownership.ProcessAlive {
		if ownership.Mode != string(mode) {
			return EnsureAgentRuntimeResult{}, fmt.Errorf("agent runtime %s is active in %s mode; stop it before starting %s mode", existing.ID, existing.Mode, mode)
		}
		if m.PrimaryConversation == nil {
			return EnsureAgentRuntimeResult{}, fmt.Errorf("active agent runtime %s has no durable root conversation", existing.ID)
		}
		session := *m.PrimaryConversation
		pathWarnings, err := a.validateRuntimeCheckout(ctx, m, *existing)
		if err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
		if err := a.validateConversationCheckout(m, session); err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
		warnings = append(warnings, pathWarnings...)
		result := EnsureAgentRuntimeResult{WorkItemID: m.ID, Runtime: *existing, Conversation: session, Capabilities: existing.Capabilities, Warnings: warnings}
		if mode == agent.ModeTUI {
			result.Terminal = &TerminalResult{WorkItemID: m.ID, Session: m.TerminalSessionName(), Checkout: m.Checkout}
		}
		return result, nil
	}
	m, session, conversationWarnings, err := a.ensureConversationCheckout(ctx, m)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	warnings = append(warnings, conversationWarnings...)
	if mode == agent.ModeTUI {
		runtimeEnv, scrub, environmentWarnings, err := a.agentRuntimeEnvironment(ctx, m, mode)
		warnings = append(warnings, environmentWarnings...)
		if err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
		terminal, err := a.ensureTerminalWorkspaceWithEnvironment(ctx, opts, m, true, runtimeEnv, scrub)
		if err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
		warnings = append(warnings, terminal.Warnings...)
		launchWarnings, err := a.launchAgentInTmuxWithEnvironment(ctx, m, session, true, runtimeEnv, scrub)
		if err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
		warnings = append(warnings, launchWarnings...)
		runtime, err := a.Store.LoadAgentRuntime(m.ID)
		if err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
		if runtime == nil {
			return EnsureAgentRuntimeResult{}, fmt.Errorf("TUI runtime was launched but runtime metadata is unavailable")
		}
		return EnsureAgentRuntimeResult{WorkItemID: m.ID, Created: true, Runtime: *runtime, Conversation: session, Capabilities: runtime.Capabilities, Terminal: &terminal, Warnings: warnings}, nil
	}
	if a.AgentRuntimeLauncher == nil {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("agent runtime launcher is not configured")
	}
	if err := a.ensureConversationNotRunning(m, session); err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	runtimeEnv, _, environmentWarnings, err := a.agentRuntimeEnvironment(ctx, m, mode)
	warnings = append(warnings, environmentWarnings...)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	runtime, err := a.prepareAgentRuntime(ctx, m, session, mode)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	self := strings.TrimSpace(a.SelfPath)
	if self == "" {
		self = "wi"
	}
	cwd := agentcore.ExecutionCWD(m, session)
	pid, err := a.AgentRuntimeLauncher.Start(agent.LaunchSpec{
		Path:    self,
		Args:    []string{"agent", "exec", "--item", m.ID, "--conversation", session.ID, "--runtime", runtime.ID, "--mode", string(mode)},
		CWD:     cwd,
		Env:     exactRuntimeEnvironment(runtimeEnv),
		LogPath: a.runtimeLogPath(m.ID, &runtime),
	})
	if err != nil {
		runtime.State = model.AgentRuntimeProblem
		runtime.UpdatedAt = a.now()
		_ = a.Store.SaveAgentRuntime(ctx, m.ID, runtime)
		return EnsureAgentRuntimeResult{}, err
	}
	current, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	if current == nil || current.ID != runtime.ID {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("agent runtime ownership changed during startup")
	}
	runtime = *current
	runtime.HostPID = pid
	if a.Process == nil {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("process inspector is not configured")
	}
	identity, err := a.Process.Info(pid)
	if err != nil {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("inspect launched agent runtime process identity: %w", err)
	}
	runtime.HostProcessGroup = identity.PGRP
	runtime.HostStartTime = identity.StartTime
	runtime.UpdatedAt = a.now()
	if err := a.Store.SaveAgentRuntime(ctx, m.ID, runtime); err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	return EnsureAgentRuntimeResult{WorkItemID: m.ID, Created: true, Runtime: runtime, Conversation: session, Capabilities: runtime.Capabilities, Warnings: warnings}, nil
}

func (a *App) validateRuntimeCheckout(ctx context.Context, m model.Manifest, runtime model.AgentRuntime) ([]string, error) {
	if runtime.WorkspaceClaimID != "" {
		claim := m.Checkout.Claim
		if claim == nil || claim.ID != runtime.WorkspaceClaimID || claim.Generation != runtime.WorkspaceClaimGeneration {
			return nil, fmt.Errorf("agent runtime %s belongs to a superseded workspace claim", runtime.ID)
		}
	}
	if runtime.Mode != string(agent.ModeTUI) || a.Tmux == nil || m.Checkout.Claim == nil || strings.TrimSpace(m.Checkout.Claim.Path) == "" {
		return nil, nil
	}
	window := "agent"
	target := m.TerminalSessionName() + ":" + window
	var panePath string
	for attempt := 0; attempt < 3; attempt++ {
		pane, err := a.Tmux.PaneInfo(ctx, target)
		if err != nil {
			return []string{"could not verify active agent pane checkout: " + err.Error()}, nil
		}
		panePath = strings.TrimSpace(pane.CurrentPath)
		if panePath != "" {
			break
		}
		if attempt < 2 {
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if panePath == "" {
		return []string{"could not verify active agent pane checkout: tmux reported an empty pane path"}, nil
	}
	checkoutPath := strings.TrimSpace(m.Checkout.Claim.Path)
	if !agentcore.CheckoutContainsPath(checkoutPath, panePath) {
		return nil, fmt.Errorf("agent runtime %s is running in %s, but work item %s now owns checkout %s; stop the runtime and start or switch the item again", runtime.ID, panePath, m.ID, checkoutPath)
	}
	return nil, nil
}

func (a *App) prepareAgentRuntime(ctx context.Context, m model.Manifest, session model.Conversation, mode agent.Mode) (model.AgentRuntime, error) {
	return a.primaryAgentService().Prepare(ctx, m, session, mode)
}

func (a *App) StopAgentRuntime(ctx context.Context, opts ResolveOptions, force bool) (StopAgentRuntimeResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return StopAgentRuntimeResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	return a.primaryAgentService().Stop(ctx, opts, force)
}

func (a *App) AgentRuntimeStatus(ctx context.Context, opts ResolveOptions) (AgentRuntimeStatusResult, error) {
	res, err := a.primaryAgentService().RuntimeStatus(ctx, opts)
	if err != nil {
		return AgentRuntimeStatusResult{}, err
	}
	return AgentRuntimeStatusResult{WorkItemID: res.WorkItemID, Runtime: res.Runtime, Online: res.Online, Capabilities: res.Capabilities, Warnings: res.Warnings}, nil
}
