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
	if _, err := a.Tmux.EnsureSession(ctx, spec); err != nil {
		return warnings, err
	}
	runtime, err := a.prepareAgentRuntime(ctx, m, session, agent.ModeTUI)
	if err != nil {
		return warnings, err
	}
	self := strings.TrimSpace(a.SelfPath)
	if self == "" {
		self = "wi"
	}
	cwd := agentcore.ExecutionCWD(m, session)
	window := "agent-" + strings.ToLower(model.ShortID(session.ID))
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

type TerminalResult = terminaladapter.Result
type TerminalCloseResult = terminaladapter.CloseResult

func (a *App) CloseTerminal(ctx context.Context, opts ResolveOptions) (TerminalCloseResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return TerminalCloseResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	return a.terminalService().Close(ctx, opts)
}
func (a *App) ensureTerminalWorkspaceWithEnvironment(ctx context.Context, opts ResolveOptions, m model.Manifest, createCheckout bool, environment map[string]string, scrub []string) (TerminalResult, error) {
	return a.terminalService().EnsureForManifest(ctx, opts, m, createCheckout, environment, scrub)
}
func (a *App) tmuxSpecWithEnvironmentAndScrub(m model.Manifest, environment map[string]string, scrub []string) tmuxpkg.SessionSpec {
	return a.terminalService().Spec(m, environment, scrub)
}
func inTmux(env map[string]string) bool { return terminaladapter.InTmux(env) }

func (a *App) tmuxTerminalExists(ctx context.Context, m model.Manifest) bool {
	if a.Tmux == nil {
		return false
	}
	exists, err := a.Tmux.HasSession(ctx, m.TerminalSessionName())
	return err == nil && exists
}
