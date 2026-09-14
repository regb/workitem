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

type TerminalStatusResult = terminaladapter.StatusResult
type TerminalResult = terminaladapter.Result
type TerminalCloseResult = terminaladapter.CloseResult

func (a *App) TerminalStatus(ctx context.Context, opts ResolveOptions) (TerminalStatusResult, error) {
	manifest, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return TerminalStatusResult{}, err
	}
	if manifest.Checkout.Executor == "" || manifest.Checkout.Executor == model.ExecutorLocal {
		return a.terminalService().Status(ctx, opts)
	}
	status, err := a.AgentRuntimeStatus(ctx, opts)
	if err != nil {
		return TerminalStatusResult{}, err
	}
	exists := status.Online && status.Runtime != nil && status.Runtime.Mode == string(agent.ModeTUI)
	return TerminalStatusResult{WorkItemID: manifest.ID, Session: manifest.TerminalSessionName(), Exists: exists, Inspected: true, Warnings: status.Warnings}, nil
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
	result := TerminalResult{WorkItemID: manifest.ID, Session: manifest.TerminalSessionName(), Checkout: manifest.Checkout, Warnings: []string{}}
	if !attach {
		return result, nil
	}
	if inTmux(opts.Env) {
		if a.Tmux == nil {
			return TerminalResult{}, fmt.Errorf("tmux terminal adapter is not configured")
		}
		self := strings.TrimSpace(a.SelfPath)
		if self == "" {
			self = "wi"
		}
		command := []string{self, "internal-executor-enter", "--executor", manifest.Checkout.Executor, "--item", manifest.ID}
		detacher, ok := a.Tmux.(interface {
			DetachClientAndRun(context.Context, []string, string) error
		})
		if !ok {
			return TerminalResult{}, fmt.Errorf("tmux terminal adapter cannot detach for direct executor access")
		}
		if err := detacher.DetachClientAndRun(ctx, command, strings.TrimSpace(opts.Env["WI_TMUX_CLIENT"])); err != nil {
			return TerminalResult{}, err
		}
	} else if err := client.AttachTerminal(ctx, manifest.ID); err != nil {
		return TerminalResult{}, err
	}
	result.Attached = true
	return result, nil
}

func (a *App) AttachExecutorTerminal(ctx context.Context, executorName, itemID string) error {
	manifest, err := a.ResolveItem(ctx, ResolveOptions{Selector: itemID})
	if err != nil {
		return err
	}
	if manifest.Checkout.Executor != executorName {
		return fmt.Errorf("work item %s is assigned to executor %s, not %s", itemID, manifest.Checkout.Executor, executorName)
	}
	client, err := a.executorClient(executorName)
	if err != nil {
		return err
	}
	if err := client.EnsureAuthority(ctx, a.ExecutorDaemonSocketPath); err != nil {
		return err
	}
	return client.AttachTerminal(ctx, itemID)
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
		client, err := a.executorClient(manifest.Checkout.Executor)
		if err != nil {
			return TerminalCloseResult{}, err
		}
		if err := client.CloseTerminal(ctx, manifest.ID); err != nil {
			return TerminalCloseResult{}, err
		}
		result := TerminalCloseResult{WorkItemID: manifest.ID, Session: manifest.TerminalSessionName(), Changed: true, Warnings: []string{}}
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
