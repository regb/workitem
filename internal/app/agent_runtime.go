package app

import (
	"context"
	"fmt"

	"os"
	"strings"

	"github.com/regb/workitem/internal/agent"
	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/model"
)

type EnsureAgentRuntimeResult struct {
	WorkItemID   string             `json:"work_item_id"`
	Created      bool               `json:"created"`
	Runtime      model.AgentRuntime `json:"runtime"`
	Conversation model.PiSession    `json:"conversation"`
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
	if !mode.Valid() {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("invalid agent runtime mode %q", mode)
	}
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	if m.State == model.StateArchived {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("work item %s is archived; set it to backlog before ensuring an agent runtime", m.ID)
	}
	m, warnings, err := a.ensureWorkspaceCheckout(ctx, opts, m, true)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	existing, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	if ownership := a.primaryAgentService().ObserveOwnership(existing); ownership.ProcessAlive {
		if ownership.Mode != string(mode) {
			return EnsureAgentRuntimeResult{}, fmt.Errorf("agent runtime %s is active in %s mode; stop it before starting %s mode", existing.ID, existing.Mode, mode)
		}
		if m.RootPiSession == nil {
			return EnsureAgentRuntimeResult{}, fmt.Errorf("active agent runtime %s has no durable root conversation", existing.ID)
		}
		session := *m.RootPiSession
		pathWarnings, err := a.validateRuntimeCheckout(ctx, m, *existing)
		if err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
		if err := a.validateConversationCheckout(m, session); err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
		warnings = append(warnings, pathWarnings...)
		result := EnsureAgentRuntimeResult{WorkItemID: m.ID, Runtime: *existing, Conversation: session, Capabilities: agent.CapabilitiesForMode(mode), Warnings: warnings}
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
		runtimeEnv, scrub, environmentWarnings, err := a.agentRuntimeEnvironment(ctx, m)
		warnings = append(warnings, environmentWarnings...)
		if err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
		terminal, err := a.ensureTerminalWorkspaceWithEnvironment(ctx, opts, m, true, runtimeEnv, scrub)
		if err != nil {
			return EnsureAgentRuntimeResult{}, err
		}
		warnings = append(warnings, terminal.Warnings...)
		launchWarnings, err := a.launchPiSessionInTmuxWithEnvironment(ctx, m, session, true, runtimeEnv, scrub)
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
		return EnsureAgentRuntimeResult{WorkItemID: m.ID, Created: true, Runtime: *runtime, Conversation: session, Capabilities: agent.CapabilitiesForMode(mode), Terminal: &terminal, Warnings: warnings}, nil
	}
	if a.AgentRuntimeLauncher == nil {
		return EnsureAgentRuntimeResult{}, fmt.Errorf("agent runtime launcher is not configured")
	}
	if err := a.ensurePiSessionNotRunning(m, session); err != nil {
		return EnsureAgentRuntimeResult{}, err
	}
	runtimeEnv, _, environmentWarnings, err := a.agentRuntimeEnvironment(ctx, m)
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
		Args:    []string{"agent", "exec", "--item", m.ID, "--session", session.ID, "--runtime", runtime.ID, "--mode", string(mode)},
		CWD:     cwd,
		Env:     mergeRuntimeEnvironment(os.Environ(), runtimeEnv),
		LogPath: a.runtimeLogPath(m.ID, &runtime),
	})
	if err != nil {
		runtime.State = model.AgentRuntimeProblem
		runtime.UpdatedAt = a.now()
		_ = a.Store.SaveAgentRuntime(ctx, m.ID, runtime)
		return EnsureAgentRuntimeResult{}, err
	}
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
	return EnsureAgentRuntimeResult{WorkItemID: m.ID, Created: true, Runtime: runtime, Conversation: session, Capabilities: agent.CapabilitiesForMode(mode), Warnings: warnings}, nil
}

func (a *App) validateRuntimeCheckout(ctx context.Context, m model.Manifest, runtime model.AgentRuntime) ([]string, error) {
	if runtime.Mode != string(agent.ModeTUI) || a.Tmux == nil || m.Checkout.Path == nil || strings.TrimSpace(*m.Checkout.Path) == "" {
		return nil, nil
	}
	window := "agent"
	pane, err := a.Tmux.PaneInfo(ctx, m.TerminalSessionName()+":"+window)
	if err != nil {
		return []string{"could not verify active agent pane checkout: " + err.Error()}, nil
	}
	checkoutPath := strings.TrimSpace(*m.Checkout.Path)
	if !agentcore.CheckoutContainsPath(checkoutPath, pane.CurrentPath) {
		return nil, fmt.Errorf("agent runtime %s is running in %s, but work item %s now owns checkout %s; stop the runtime and start or switch the item again", runtime.ID, pane.CurrentPath, m.ID, checkoutPath)
	}
	return nil, nil
}

func (a *App) prepareAgentRuntime(ctx context.Context, m model.Manifest, session model.PiSession, mode agent.Mode) (model.AgentRuntime, error) {
	return a.primaryAgentService().Prepare(ctx, m, session, mode)
}

func (a *App) StopAgentRuntime(ctx context.Context, opts ResolveOptions, force bool) (StopAgentRuntimeResult, error) {
	return a.primaryAgentService().Stop(ctx, opts, force)
}

func (a *App) AgentRuntimeStatus(ctx context.Context, opts ResolveOptions) (AgentRuntimeStatusResult, error) {
	res, err := a.primaryAgentService().RuntimeStatus(ctx, opts)
	if err != nil {
		return AgentRuntimeStatusResult{}, err
	}
	return AgentRuntimeStatusResult{WorkItemID: res.WorkItemID, Runtime: res.Runtime, Online: res.Online, Capabilities: res.Capabilities, Warnings: res.Warnings}, nil
}
