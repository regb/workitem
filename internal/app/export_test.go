package app

// The old start composition, kept for the architecture tests only: they
// exercise lifecycle, checkout and runtime invariants through the same
// primitives the daemon composes (internal/daemonapi startItem), and
// production has that one start path.

import (
	"context"
	"fmt"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

type StartResult struct {
	Transition StateTransitionResult `json:"transition"`
	Workspace  CompositionResult     `json:"workspace"`
}

// StartWorkItem is a cross-cutting use case: persist the lifecycle transition
// first, then materialize the worktree and selected agent/access resources.
func (a *App) StartWorkItem(ctx context.Context, opts ResolveOptions, force, attach bool) (StartResult, error) {
	return a.StartWorkItemInMode(ctx, opts, force, attach, agent.ModeTUI)
}

func (a *App) StartWorkItemInMode(ctx context.Context, opts ResolveOptions, force, attach bool, mode agent.Mode) (StartResult, error) {
	if mode == "" {
		mode = agent.ModeTUI
	}
	if !mode.Valid() {
		return StartResult{}, fmt.Errorf("invalid agent runtime mode %q", mode)
	}
	if err := a.ValidateItemAgentConfig(ctx, opts); err != nil {
		return StartResult{}, err
	}
	transition, err := a.transitionState(ctx, opts, model.StateActive, "work_item.started", force)
	if err != nil {
		return StartResult{}, err
	}
	return a.materializeTransitionAgent(ctx, opts, transition, attach, mode)
}

// ResumeWorkItem is a cross-cutting use case over durable state, worktree,
// primary-agent, and optional tmux-access primitives.
func (a *App) ResumeWorkItem(ctx context.Context, opts ResolveOptions, attach bool) (StartResult, error) {
	return a.ResumeWorkItemInMode(ctx, opts, attach, agent.ModeTUI)
}

func (a *App) ResumeWorkItemInMode(ctx context.Context, opts ResolveOptions, attach bool, mode agent.Mode) (StartResult, error) {
	if mode == "" {
		mode = agent.ModeTUI
	}
	if !mode.Valid() {
		return StartResult{}, fmt.Errorf("invalid agent runtime mode %q", mode)
	}
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return StartResult{}, err
	}
	if m.State == model.StateBacklog {
		return StartResult{}, fmt.Errorf("cannot resume backlog item; run `wi start` first")
	}
	if err := a.ValidateItemAgentConfig(ctx, opts); err != nil {
		return StartResult{}, err
	}
	transition, err := a.transitionState(ctx, opts, model.StateActive, "work_item.resumed", false)
	if err != nil {
		return StartResult{}, err
	}
	return a.materializeTransitionAgent(ctx, opts, transition, attach, mode)
}

func (a *App) materializeTransitionAgent(ctx context.Context, opts ResolveOptions, transition StateTransitionResult, attach bool, mode agent.Mode) (StartResult, error) {
	if mode == "" {
		mode = agent.ModeTUI
	}
	runtimeOpts := opts
	runtimeOpts.Selector = transition.Manifest.ID
	// The runtime's checkout is set up first; the item stays active and
	// claimed when setup fails.
	runtime, err := a.EnsureAgentRuntime(ctx, runtimeOpts, mode)
	if err != nil {
		return StartResult{Transition: transition}, err
	}
	manifest, err := a.Store.LoadManifest(transition.Manifest.ID)
	if err != nil {
		return StartResult{}, err
	}
	workspace := CompositionResult{
		WorkItemID: transition.Manifest.ID, Checkout: manifest.Checkout,
		Conversation: manifest.PrimaryConversation, AgentLaunched: runtime.Created,
		AgentRuntime: &runtime.Runtime, Manifest: manifest, Warnings: runtime.Warnings,
	}
	if mode == agent.ModeTUI {
		workspace.Terminal = runtime.Terminal
		if attach {
			terminal, err := a.EnterTerminal(ctx, ResolveOptions{Selector: manifest.ID, CWD: opts.CWD, Env: opts.Env}, true)
			if err != nil {
				return StartResult{}, err
			}
			workspace.Terminal = &terminal
			workspace.Warnings = append(workspace.Warnings, terminal.Warnings...)
		}
	}
	return StartResult{Transition: transition, Workspace: workspace}, nil
}

type NewAgentWorkItemResult struct {
	New     NewWorkItemResult  `json:"new"`
	Start   StartResult        `json:"start"`
	Control AgentControlResult `json:"control"`
}

// NewAgentWorkItem creates a durable item, starts a runtime without attaching,
// and submits a separate startup message. Later failures retain partial outcomes.
func (a *App) NewAgentWorkItem(ctx context.Context, opts NewWorkItemOptions, mode agent.Mode, message, actor string) (NewAgentWorkItemResult, error) {
	prompt := a.AgentStartupMessage(message)
	if mode == "" {
		mode = agent.ModeTUI
	}
	if !mode.Valid() {
		return NewAgentWorkItemResult{}, fmt.Errorf("invalid agent runtime mode %q", mode)
	}
	if err := a.ValidateAgentConfig(ctx, opts.Agent, opts.AgentSettings); err != nil {
		return NewAgentWorkItemResult{}, err
	}
	created, err := a.NewWorkItem(ctx, opts)
	if err != nil {
		return NewAgentWorkItemResult{}, err
	}
	result := NewAgentWorkItemResult{New: created}
	resolve := ResolveOptions{Selector: created.Manifest.ID, CWD: opts.CWD, Env: opts.Env}
	started, err := a.StartWorkItemInMode(ctx, resolve, false, false, mode)
	if err != nil {
		return result, fmt.Errorf("created work item %s (%s), but could not start its agent: %w", created.Manifest.Slug, created.Manifest.ID, err)
	}
	result.Start = started
	if err := a.waitForAgentRuntimeReady(ctx, created.Manifest.ID, started.Workspace.AgentRuntime); err != nil {
		return result, fmt.Errorf("created and started work item %s (%s), but could not submit its prompt: %w", created.Manifest.Slug, created.Manifest.ID, err)
	}
	controlled, err := a.AgentControl(ctx, AgentControlOptions{ResolveOptions: resolve, CommandType: agent.CommandPrompt, Message: prompt, Actor: actor, ExpectedRuntimeID: started.Workspace.AgentRuntime.ID})
	result.Control = controlled
	if err != nil {
		return result, fmt.Errorf("created and started work item %s (%s), but could not submit its prompt: %w", created.Manifest.Slug, created.Manifest.ID, err)
	}
	return result, nil
}

func (a *App) WaitForAgentRuntimeReady(ctx context.Context, itemID string, runtime *model.AgentRuntime) error {
	return a.waitForAgentRuntimeReady(ctx, itemID, runtime)
}

func (a *App) transitionState(ctx context.Context, opts ResolveOptions, target, eventType string, force bool) (StateTransitionResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return StateTransitionResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	return a.itemService().TransitionState(ctx, opts, target, eventType, force, a.DeepWorkConfig.MaxActive)
}
