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

func (a *App) launchPiSessionInTmuxWithEnvironment(ctx context.Context, m model.Manifest, session model.PiSession, reuseAgent bool, runtimeEnv map[string]string, scrub []string) ([]string, error) {
	warnings := []string{}
	if a.Tmux == nil {
		return warnings, fmt.Errorf("tmux adapter is not configured")
	}
	if err := a.ensurePiSessionNotRunning(m, session); err != nil {
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
	window := "pi-" + strings.ToLower(model.ShortID(session.ID))
	if err := a.Tmux.LaunchCommand(ctx, tmuxpkg.LaunchSpec{SessionName: m.TerminalSessionName(), WindowName: window, CWD: cwd,
		Command: []string{self, "agent", "exec", "--item", m.ID, "--session", session.ID, "--runtime", runtime.ID, "--mode", string(agent.ModeTUI)},
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

func (a *App) TerminalStatus(ctx context.Context, opts ResolveOptions) (TerminalStatusResult, error) {
	return a.terminalService().Status(ctx, opts)
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
