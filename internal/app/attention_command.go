package app

import (
	"context"
	"errors"

	attentioncore "github.com/regb/workitem/internal/app/core/attention"
	"github.com/regb/workitem/internal/model"
)

// RecordAttentionDefer is a core attention primitive. It records a priority
// fact for working work without inspecting agent, process, tmux, or worktree
// state. Interactive eligibility belongs to tmux navigation porcelain.
func (a *App) RecordAttentionDefer(ctx context.Context, opts ResolveOptions) (DeferResult, error) {
	res, err := a.attentionService().RecordDefer(ctx, opts)
	if err != nil {
		var stateErr *attentioncore.StateError
		if errors.As(err, &stateErr) {
			return DeferResult{}, &NotNeedsAttentionError{WorkItemID: stateErr.WorkItemID, State: stateErr.State}
		}
		return DeferResult{}, err
	}
	piSession, warnings := a.inspectPiSession(res.Manifest, a.now())
	activity, activityWarnings := a.attentionActivity(res.WorkItemID, AgentStatusResult{PiSession: piSession})
	warnings = append(warnings, activityWarnings...)
	warnings = append(warnings, res.Warnings...)
	activity.LastDeferredAt = timePointer(res.DeferredAt)
	return DeferResult{WorkItemID: res.WorkItemID, DeferredAt: res.DeferredAt,
		Activity: activity, Manifest: res.Manifest, Warnings: warnings}, nil
}

// DeferWorkItem validates live NEEDS ATTENTION eligibility before recording.
// It is used by tmux navigation; the public core command calls
// RecordAttentionDefer directly.
func (a *App) DeferWorkItem(ctx context.Context, opts ResolveOptions) (DeferResult, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return DeferResult{}, err
	}
	if m.State != model.StateWorking {
		return DeferResult{}, &NotNeedsAttentionError{WorkItemID: m.ID, State: m.State}
	}
	status, err := a.AgentStatus(ctx, AgentStatusOptions{ResolveOptions: ResolveOptions{Selector: m.ID, CWD: opts.CWD, Env: opts.Env}})
	if err != nil {
		return DeferResult{}, err
	}
	agent := a.workAgentStatus(status)
	if agent.Bucket != "needs_attention" || (status.Worktree != nil && status.Worktree.Status == "problem") {
		return DeferResult{}, &NotNeedsAttentionError{WorkItemID: m.ID, State: m.State, Agent: status.Status, Worktree: worktreeStatusName(status.Worktree)}
	}
	return a.recordAttentionDefer(ctx, m, status, nil)
}

func (a *App) recordAttentionDefer(ctx context.Context, m model.Manifest, status AgentStatusResult, warnings []string) (DeferResult, error) {
	now := a.now()
	if err := a.Store.AppendEvent(ctx, m.ID, model.NewEvent(now, attentionDeferredEvent, "user", map[string]any{"deferred_at": now})); err != nil {
		return DeferResult{}, err
	}
	activity, activityWarnings := a.attentionActivity(m.ID, status)
	warnings = append(warnings, activityWarnings...)
	activity.LastDeferredAt = timePointer(now)
	return DeferResult{WorkItemID: m.ID, DeferredAt: now, Activity: activity, Manifest: m, Warnings: warnings}, nil
}
