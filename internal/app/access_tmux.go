package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

type gatewaySessionTmux interface {
	EnsureGatewaySession(context.Context, tmuxpkg.SessionSpec, []string, []string) (bool, error)
}

type targetedTmuxAccess interface {
	AttachOrSwitchClient(context.Context, string, bool, string) error
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
	cwd := agentcore.ExecutionCWD(m, session)
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

func (a *App) TerminalStatus(ctx context.Context, opts ResolveOptions) (TerminalStatusResult, error) {
	return a.terminalService().Status(ctx, opts)
}
func (a *App) EnsureTerminal(ctx context.Context, opts ResolveOptions) (TerminalResult, error) {
	manifest, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return TerminalResult{}, err
	}
	if manifest.Checkout.Executor != "" && manifest.Checkout.Executor != model.ExecutorLocal {
		return a.ensureRemoteTerminalAccess(ctx, opts, manifest, false)
	}
	return a.terminalService().Ensure(ctx, opts)
}
func (a *App) EnterTerminal(ctx context.Context, opts ResolveOptions, attach bool) (TerminalResult, error) {
	manifest, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return TerminalResult{}, err
	}
	if manifest.Checkout.Executor != "" && manifest.Checkout.Executor != model.ExecutorLocal {
		return a.ensureRemoteTerminalAccess(ctx, opts, manifest, attach)
	}
	return a.terminalService().Enter(ctx, opts, attach)
}
func (a *App) ensureRemoteTerminalAccess(ctx context.Context, opts ResolveOptions, manifest model.Manifest, attach bool) (TerminalResult, error) {
	client, err := a.executorClient(manifest.Checkout.Executor)
	if err != nil {
		return TerminalResult{}, err
	}
	if err := client.EnsureAuthority(ctx, a.ExecutorDaemonSocketPath); err != nil {
		return TerminalResult{}, err
	}
	agentCommand, shellCommand, err := client.TerminalCommands(manifest.ID)
	if err != nil {
		return TerminalResult{}, err
	}
	if a.Tmux == nil {
		return TerminalResult{}, fmt.Errorf("tmux terminal adapter is not configured")
	}

	cwd := manifest.Repository.OperationalRoot()
	if info, statErr := os.Stat(cwd); statErr != nil || !info.IsDir() {
		cwd = strings.TrimSpace(a.Home)
	}
	if cwd == "" {
		cwd, _ = os.UserHomeDir()
	}
	environment := map[string]string{"WI_ID": manifest.ID, "WI_EXECUTOR": manifest.Checkout.Executor}
	if a.Store != nil {
		environment["WI_DIR"] = a.Store.ItemDir(manifest.ID)
	}
	for _, key := range []string{"HOME", "PATH", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		if value := strings.TrimSpace(a.Environment[key]); value != "" {
			environment[key] = value
		}
	}
	if root := manifest.Repository.OperationalRoot(); root != "" {
		environment["WI_REPOSITORY"] = root
	}
	spec := tmuxpkg.SessionSpec{Name: manifest.TerminalSessionName(), CWD: filepath.Clean(cwd), Env: environment, Scrub: []string{"WI_WORKTREE", "WI_EXECUTOR_DAEMON_SOCKET", "WI_AGENT_DAEMON_SOCKET", "WI_TMUX_SOCKET", "WI_TMUX_CONFIG"}}
	gateway, ok := a.Tmux.(gatewaySessionTmux)
	if !ok {
		return TerminalResult{}, fmt.Errorf("tmux terminal adapter cannot create executor gateway sessions")
	}
	created, err := gateway.EnsureGatewaySession(ctx, spec, agentCommand, shellCommand)
	if err != nil {
		return TerminalResult{}, err
	}
	result := TerminalResult{WorkItemID: manifest.ID, Session: manifest.TerminalSessionName(), Created: created, Checkout: manifest.Checkout, Warnings: []string{}}
	if !attach {
		return result, nil
	}
	inLocalTmux := inTmux(opts.Env)
	originatingClient := strings.TrimSpace(opts.Env["WI_TMUX_CLIENT"])
	if switcher, ok := a.Tmux.(targetedTmuxAccess); ok && originatingClient != "" {
		err = switcher.AttachOrSwitchClient(ctx, result.Session, inLocalTmux, originatingClient)
	} else {
		err = a.Tmux.AttachOrSwitch(ctx, result.Session, inLocalTmux)
	}
	if err != nil {
		return TerminalResult{}, err
	}
	result.Attached = true
	return result, nil
}

func (a *App) enterExistingTerminal(ctx context.Context, opts ResolveOptions, attach bool) (TerminalResult, error) {
	return a.terminalService().EnterExisting(ctx, opts, attach)
}
func (a *App) CloseTerminal(ctx context.Context, opts ResolveOptions) (TerminalCloseResult, error) {
	manifest, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return TerminalCloseResult{}, err
	}
	if manifest.Checkout.Executor != "" && manifest.Checkout.Executor != model.ExecutorLocal {
		result := TerminalCloseResult{WorkItemID: manifest.ID, Session: manifest.TerminalSessionName(), Warnings: []string{}}
		localExists := false
		if a.Tmux != nil {
			localExists, err = a.Tmux.HasSession(ctx, result.Session)
			if err != nil {
				return TerminalCloseResult{}, fmt.Errorf("inspect local executor gateway: %w", err)
			}
			if localExists && inTmux(opts.Env) {
				if current, currentErr := a.Tmux.CurrentSession(ctx); currentErr != nil {
					return TerminalCloseResult{}, currentErr
				} else if current == result.Session {
					return TerminalCloseResult{}, fmt.Errorf("cannot close current tmux terminal %s; run this command from outside it", result.Session)
				}
			}
		}
		client, clientErr := a.executorClient(manifest.Checkout.Executor)
		if clientErr != nil {
			return TerminalCloseResult{}, clientErr
		}
		if closeErr := client.CloseTerminal(ctx, manifest.ID); closeErr != nil {
			return TerminalCloseResult{}, closeErr
		}
		result.Changed = true
		if localExists {
			if err := a.Tmux.KillSession(ctx, result.Session); err != nil {
				return TerminalCloseResult{}, err
			}
			result.Changed = true
		}
		_ = a.Store.RemoveTerminalRuntime(ctx, manifest.ID)
		return result, nil
	}
	return a.terminalService().Close(ctx, opts)
}
func (a *App) ensureTerminalWorkspaceWithEnvironment(ctx context.Context, opts ResolveOptions, m model.Manifest, createCheckout bool, environment map[string]string, scrub []string) (TerminalResult, error) {
	return a.terminalService().EnsureForManifestWithEnvironmentAndScrub(ctx, opts, m, createCheckout, environment, scrub)
}
func (a *App) tmuxSpecWithEnvironmentAndScrub(m model.Manifest, environment map[string]string, scrub []string) tmuxpkg.SessionSpec {
	return a.terminalService().SpecWithEnvironmentAndScrub(m, environment, scrub)
}
func inTmux(env map[string]string) bool { return terminaladapter.InTmux(env) }
