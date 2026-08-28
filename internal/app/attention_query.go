package app

import (
	"context"
	"time"

	viewapp "github.com/regb/workitem/internal/app/view"
)

func (a *App) EnrichWorkList(ctx context.Context, result *WorkListResult, resolve ResolveOptions) []string {
	observe := a.WorkListObserver
	if observe == nil {
		observe = func(ctx context.Context, itemID string) (viewapp.Observation, error) {
			status, err := a.AgentStatus(ctx, AgentStatusOptions{ResolveOptions: ResolveOptions{Selector: itemID, CWD: resolve.CWD, Env: resolve.Env}})
			if err != nil {
				return viewapp.Observation{}, err
			}
			activity, activityWarnings := a.attentionActivity(itemID, status)
			return viewapp.Observation{Status: status.Status, Reason: status.Reason, ProcessOnline: status.Process.Online, TurnState: status.PiSession.InferredTurnState, LastActivityAgeSeconds: status.PiSession.LastActivityAgeSeconds, Worktree: viewWorktreeStatus(status.Worktree), Activity: activity, ActivityWarnings: activityWarnings, Warnings: status.Warnings}, nil
		}
	}
	return a.viewService().Enrich(ctx, result, observe, func(_ string, observation viewapp.Observation) (viewapp.Activity, []string) {
		return observation.Activity, observation.ActivityWarnings
	}, viewapp.Markers{Busy: a.AgentStatusConfig.Markers.Busy, Idle: a.AgentStatusConfig.Markers.Idle, Problem: a.AgentStatusConfig.Markers.Problem}, a.AttentionConfig.Priority)
}

func (a *App) AttentionQueue(ctx context.Context, opts AttentionQueueOptions) (AttentionQueueResult, error) {
	result := a.WorkList(opts.WorkListOptions)
	result.Warnings = append(result.Warnings, a.EnrichWorkList(ctx, &result, opts.ResolveOptions)...)
	queue := viewapp.Queue(result, a.AttentionConfig.Priority)
	candidates := make([]AttentionCandidate, len(queue.Candidates))
	for i, c := range queue.Candidates {
		candidates[i] = AttentionCandidate{Item: c.Item, Activity: c.Activity, Rank: c.Rank}
	}
	return AttentionQueueResult{Strategy: queue.Strategy, Candidates: candidates, Warnings: queue.Warnings}, nil
}

func (a *App) WorkItemActivity(ctx context.Context, opts ResolveOptions) (WorkItemActivityResult, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return WorkItemActivityResult{}, err
	}
	session, warnings := a.inspectPiSession(m, a.now())
	activity, extra := a.attentionActivity(m.ID, AgentStatusResult{PiSession: session})
	return WorkItemActivityResult{WorkItemID: m.ID, Activity: activity, Warnings: append(warnings, extra...)}, nil
}

func (a *App) attentionActivity(itemID string, status AgentStatusResult) (AttentionActivity, []string) {
	var requested, completed *time.Time
	if status.PiSession.LastUserPrompt != nil && !status.PiSession.LastUserPrompt.Timestamp.IsZero() {
		requested = timePointer(status.PiSession.LastUserPrompt.Timestamp)
	}
	if status.PiSession.LastTerminalAssistant != nil && !status.PiSession.LastTerminalAssistant.Timestamp.IsZero() {
		completed = timePointer(status.PiSession.LastTerminalAssistant.Timestamp)
	}
	activity, warnings := a.attentionService().FoldActivity(itemID, requested, completed)
	return AttentionActivity(activity), warnings
}

func (a *App) workAgentStatus(status AgentStatusResult) WorkAgentStatus {
	observation := viewapp.Observation{Status: status.Status, Reason: status.Reason, ProcessOnline: status.Process.Online, TurnState: status.PiSession.InferredTurnState, LastActivityAgeSeconds: status.PiSession.LastActivityAgeSeconds}
	marker := viewapp.Markers{Busy: a.AgentStatusConfig.Markers.Busy, Idle: a.AgentStatusConfig.Markers.Idle, Problem: a.AgentStatusConfig.Markers.Problem}
	return viewapp.ProjectAgent(observation, marker)
}
func viewWorktreeStatus(status *WorktreeStatus) *viewapp.WorktreeStatus {
	if status == nil {
		return nil
	}
	return &viewapp.WorktreeStatus{Status: status.Status, Reason: status.Reason, CheckoutPath: status.CheckoutPath, Head: status.Head, ExpectedBranch: status.ExpectedBranch, CurrentBranch: status.CurrentBranch, BranchMatches: status.BranchMatches, BranchMismatch: status.BranchMismatch, Dirty: status.Dirty, HasChanges: status.HasChanges}
}
func worktreeStatusName(status *WorktreeStatus) string {
	if status == nil || status.Status == "" {
		return "unknown"
	}
	return status.Status
}
func timePointer(value time.Time) *time.Time { copy := value; return &copy }
