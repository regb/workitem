package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

const (
	defaultAgentRuntimeReadyTimeout  = 10 * time.Second
	defaultAgentRuntimeReadyInterval = 100 * time.Millisecond
)

type NewAgentWorkItemResult struct {
	New     NewWorkItemResult  `json:"new"`
	Start   StartResult        `json:"start"`
	Control AgentControlResult `json:"control"`
}

// NewAgentWorkItem creates a durable item, starts the selected runtime without
// attaching the caller, and submits the same self-contained description as its
// initial request. Creation and startup are durable partial outcomes if a later
// stage fails.
func (a *App) NewAgentWorkItem(ctx context.Context, opts NewWorkItemOptions, mode agent.Mode, actor string) (NewAgentWorkItemResult, error) {
	prompt := strings.TrimSpace(opts.Description)
	if prompt == "" {
		return NewAgentWorkItemResult{}, fmt.Errorf("agent prompt is required")
	}
	if mode == "" {
		mode = agent.ModeTUI
	}
	if !mode.Valid() {
		return NewAgentWorkItemResult{}, fmt.Errorf("invalid agent runtime mode %q", mode)
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
	controlled, err := a.AgentControl(ctx, AgentControlOptions{ResolveOptions: resolve, CommandType: agent.CommandPrompt, Message: prompt, Actor: actor})
	result.Control = controlled
	if err != nil {
		return result, fmt.Errorf("created and started work item %s (%s), but could not submit its prompt: %w", created.Manifest.Slug, created.Manifest.ID, err)
	}
	return result, nil
}

func (a *App) WaitForAgentRuntimeReady(ctx context.Context, itemID string, runtime *model.AgentRuntime) error {
	return a.waitForAgentRuntimeReady(ctx, itemID, runtime)
}

func (a *App) waitForAgentRuntimeReady(ctx context.Context, itemID string, runtime *model.AgentRuntime) error {
	if runtime == nil {
		return fmt.Errorf("agent runtime metadata is unavailable")
	}
	timeout := a.AgentRuntimeReadyTimeout
	if timeout == 0 {
		timeout = defaultAgentRuntimeReadyTimeout
	}
	interval := a.AgentRuntimeReadyInterval
	if interval <= 0 {
		interval = defaultAgentRuntimeReadyInterval
	}
	socketPath := a.runtimeControlSocketPath(itemID, runtime)
	if socketPath == "" {
		return fmt.Errorf("agent runtime %s has no control socket", runtime.ID)
	}
	ready := func() (bool, error) {
		current, err := a.Store.LoadAgentRuntime(itemID)
		if err != nil {
			return false, err
		}
		if current == nil {
			return false, fmt.Errorf("agent runtime %s metadata disappeared", runtime.ID)
		}
		if current.State == model.AgentRuntimeProblem || current.State == model.AgentRuntimeStopped {
			return false, fmt.Errorf("agent runtime %s is %s before accepting the initial prompt", runtime.ID, current.State)
		}
		if _, err := os.Stat(socketPath); err == nil {
			return true, nil
		}
		return false, nil
	}
	if ok, err := ready(); err != nil {
		return err
	} else if ok {
		return nil
	}
	if timeout < 0 {
		return fmt.Errorf("agent runtime %s is not ready for control", runtime.ID)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		if ok, err := ready(); err != nil {
			return err
		} else if ok {
			return nil
		}
	}
	logPath := a.runtimeLogPath(itemID, runtime)
	return fmt.Errorf("agent runtime %s did not become ready within %s; inspect %s", runtime.ID, timeout, logPath)
}
