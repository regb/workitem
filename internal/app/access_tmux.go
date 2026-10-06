package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/regb/workitem/internal/agent"
	terminaladapter "github.com/regb/workitem/internal/app/adapter/tmux/terminal"
	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/model"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
)

type commandSessionTmux interface {
	EnsureCommandSession(context.Context, tmuxpkg.SessionSpec, string, []string) (bool, error)
}

type copyModeTmux interface {
	CopyMode(context.Context, string) error
}

func (a *App) launchAgentInTmuxWithEnvironment(ctx context.Context, m model.Manifest, session model.Conversation, reuseAgent bool, runtimeEnv map[string]string, scrub []string) ([]string, error) {
	warnings := []string{}
	if a.Tmux == nil {
		return warnings, fmt.Errorf("tmux adapter is not configured")
	}
	if err := a.ensureConversationNotRunning(m, session); err != nil {
		return warnings, err
	}
	spec := a.tmuxSpecWithEnvironmentAndScrub(m, runtimeEnv, scrub)
	for key, value := range runtimeEnv {
		spec.Env[key] = value
	}
	self := strings.TrimSpace(a.SelfPath)
	if self == "" {
		self = "wi"
	}
	cwd := agentcore.HostExecutionCWD(m, session, a.Store.ItemDir(m.ID))
	window := "agent-" + strings.ToLower(model.ShortID(session.ID))
	if a.SinglePaneTUITerminal {
		runtime, err := a.prepareAgentRuntime(ctx, m, session, agent.ModeTUI)
		if err != nil {
			return warnings, err
		}
		command := []string{self, "agent", "exec", "--item", m.ID, "--conversation", session.ID, "--runtime", runtime.ID, "--mode", string(agent.ModeTUI)}
		host, ok := a.Tmux.(commandSessionTmux)
		if !ok {
			return warnings, fmt.Errorf("tmux terminal adapter cannot create a persistent TUI session")
		}
		created, launchErr := host.EnsureCommandSession(ctx, spec, "agent", command)
		if launchErr == nil && !created {
			launchErr = a.Tmux.LaunchCommand(ctx, tmuxpkg.LaunchSpec{SessionName: m.TerminalSessionName(), CWD: cwd, Command: command, Env: spec.Env, Scrub: spec.Scrub, ReuseAgentWindow: true})
		}
		if launchErr != nil {
			runtime.State, runtime.UpdatedAt = model.AgentRuntimeProblem, a.now()
			_ = a.Store.SaveAgentRuntime(ctx, m.ID, runtime)
			return warnings, launchErr
		}
		return append(warnings, a.recordPrimaryTerminalRuntime(ctx, m, "agent")...), nil
	}
	if _, err := a.Tmux.EnsureSession(ctx, spec); err != nil {
		return warnings, err
	}
	runtime, err := a.prepareAgentRuntime(ctx, m, session, agent.ModeTUI)
	if err != nil {
		return warnings, err
	}
	if err := a.Tmux.LaunchCommand(ctx, tmuxpkg.LaunchSpec{SessionName: m.TerminalSessionName(), WindowName: window, CWD: cwd,
		Command: []string{self, "agent", "exec", "--item", m.ID, "--conversation", session.ID, "--runtime", runtime.ID, "--mode", string(agent.ModeTUI)},
		Env:     spec.Env, Scrub: spec.Scrub, ReuseAgentWindow: reuseAgent}); err != nil {
		runtime.State, runtime.UpdatedAt = model.AgentRuntimeProblem, a.now()
		_ = a.Store.SaveAgentRuntime(ctx, m.ID, runtime)
		return warnings, err
	}
	if reuseAgent {
		window = "agent"
	}
	return append(warnings, a.recordPrimaryTerminalRuntime(ctx, m, window)...), nil
}

type TerminalStatusResult = terminaladapter.StatusResult
type TerminalResult = terminaladapter.Result
type TerminalCloseResult = terminaladapter.CloseResult

type TerminalCopyModeResult struct {
	WorkItemID string `json:"work_item_id"`
	Session    string `json:"session"`
	Changed    bool   `json:"changed"`
}

func (a *App) TerminalStatus(ctx context.Context, opts ResolveOptions) (TerminalStatusResult, error) {
	return a.terminalService().Status(ctx, opts)
}
func (a *App) EnterTerminalCopyMode(ctx context.Context, opts ResolveOptions, attachmentID string) (TerminalCopyModeResult, error) {
	manifest, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return TerminalCopyModeResult{}, err
	}
	result := TerminalCopyModeResult{WorkItemID: manifest.ID, Session: manifest.TerminalSessionName()}
	copier, ok := a.Tmux.(copyModeTmux)
	if !ok {
		return result, fmt.Errorf("tmux terminal adapter cannot enter copy mode")
	}
	target := strings.TrimSpace(attachmentID)
	if target == "" {
		target = manifest.TerminalSessionName() + ":agent"
	}
	if err := copier.CopyMode(ctx, target); err != nil {
		return result, err
	}
	result.Changed = true
	return result, nil
}

func (a *App) EnsureTerminal(ctx context.Context, opts ResolveOptions) (TerminalResult, error) {
	return a.terminalService().Ensure(ctx, opts)
}
func (a *App) EnterTerminal(ctx context.Context, opts ResolveOptions, attach bool) (TerminalResult, error) {
	return a.terminalService().Enter(ctx, opts, attach)
}
func (a *App) enterExistingTerminal(ctx context.Context, opts ResolveOptions, attach bool) (TerminalResult, error) {
	return a.terminalService().EnterExisting(ctx, opts, attach)
}
func (a *App) CloseTerminal(ctx context.Context, opts ResolveOptions) (TerminalCloseResult, error) {
	return a.terminalService().Close(ctx, opts)
}
func (a *App) ensureTerminalWorkspaceWithEnvironment(ctx context.Context, opts ResolveOptions, m model.Manifest, createCheckout bool, environment map[string]string, scrub []string) (TerminalResult, error) {
	return a.terminalService().EnsureForManifestWithEnvironmentAndScrub(ctx, opts, m, createCheckout, environment, scrub)
}
func (a *App) tmuxSpecWithEnvironmentAndScrub(m model.Manifest, environment map[string]string, scrub []string) tmuxpkg.SessionSpec {
	return a.terminalService().SpecWithEnvironmentAndScrub(m, environment, scrub)
}
func inTmux(env map[string]string) bool { return terminaladapter.InTmux(env) }
