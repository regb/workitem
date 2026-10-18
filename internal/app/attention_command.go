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
	activity, warnings := a.attentionActivity(ctx, res.WorkItemID)
	warnings = append(warnings, res.Warnings...)
	activity.LastDeferredAt = latestAttentionTime(activity.LastDeferredAt, res.DeferredAt)
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
	if m.State != model.StateActive {
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
	return a.RecordAttentionDefer(ctx, ResolveOptions{Selector: m.ID, CWD: opts.CWD, Env: opts.Env})
}

// MarkRead records that the operator has seen the agent's responses so far.
// It is an explicit attention fact; clients decide when opening a
// conversation counts as reading it.
func (a *App) MarkRead(ctx context.Context, opts ResolveOptions) (ReadMarkerResult, error) {
	return a.recordReadMarker(ctx, opts, false)
}

// MarkUnread restores the unread signal for the latest response.
func (a *App) MarkUnread(ctx context.Context, opts ResolveOptions) (ReadMarkerResult, error) {
	return a.recordReadMarker(ctx, opts, true)
}

func (a *App) recordReadMarker(ctx context.Context, opts ResolveOptions, unread bool) (ReadMarkerResult, error) {
	record := a.attentionService().RecordRead
	if unread {
		record = a.attentionService().RecordUnread
	}
	m, at, err := record(ctx, opts)
	if err != nil {
		return ReadMarkerResult{}, err
	}
	activity, warnings := a.attentionActivity(ctx, m.ID)
	if unread {
		activity.LastUnreadAt = latestAttentionTime(activity.LastUnreadAt, at)
	} else {
		activity.LastReadAt = latestAttentionTime(activity.LastReadAt, at)
	}
	return ReadMarkerResult{WorkItemID: m.ID, Unread: activity.Unread(), MarkedAt: at, Activity: activity, Warnings: warnings}, nil
}
