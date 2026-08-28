package app

import (
	"context"
	"fmt"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

type StartNewWorkItemResult struct {
	New   NewWorkItemResult `json:"new"`
	Start StartResult       `json:"start"`
}

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
	transition, err := a.transitionState(ctx, opts, model.StateWorking, "work_item.started", force)
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
	transition, err := a.transitionState(ctx, opts, model.StateWorking, "work_item.resumed", false)
	if err != nil {
		return StartResult{}, err
	}
	return a.materializeTransitionAgent(ctx, opts, transition, attach, mode)
}

// MaterializeTransitionAgent performs the external workspace/runtime phase
// after a durable lifecycle transition has committed.
func (a *App) MaterializeTransitionAgent(ctx context.Context, opts ResolveOptions, transition StateTransitionResult, attach bool, mode agent.Mode) (StartResult, error) {
	return a.materializeTransitionAgent(ctx, opts, transition, attach, mode)
}

func (a *App) materializeTransitionAgent(ctx context.Context, opts ResolveOptions, transition StateTransitionResult, attach bool, mode agent.Mode) (StartResult, error) {
	if mode == "" {
		mode = agent.ModeTUI
	}
	runtimeOpts := opts
	runtimeOpts.Selector = transition.Manifest.ID
	runtime, err := a.EnsureAgentRuntime(ctx, runtimeOpts, mode)
	if err != nil {
		return StartResult{}, err
	}
	manifest, err := a.Store.LoadManifest(transition.Manifest.ID)
	if err != nil {
		return StartResult{}, err
	}
	workspace := CompositionResult{
		WorkItemID: transition.Manifest.ID, Checkout: manifest.Checkout,
		PiSession: manifest.RootPiSession, PiLaunched: runtime.Created,
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
