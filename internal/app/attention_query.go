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
			activity, activityWarnings := a.attentionActivity(ctx, itemID)
			return viewapp.Observation{Status: status.Status, Reason: status.Reason, ProcessOnline: status.Process.Online, TurnState: status.Conversation.InferredTurnState, LastActivityAgeSeconds: status.Conversation.LastActivityAgeSeconds, Worktree: viewWorktreeStatus(status.Worktree), Activity: activity, ActivityWarnings: activityWarnings, Warnings: status.Warnings}, nil
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
	activity, warnings := a.attentionActivity(ctx, m.ID)
	return WorkItemActivityResult{WorkItemID: m.ID, Activity: activity, Warnings: warnings}, nil
}

// Activity queries use the coordinator snapshot. Event replay belongs to
// projection rebuilds, not interactive commands or navigation.
func (a *App) attentionActivity(ctx context.Context, itemID string) (AttentionActivity, []string) {
	if a.AttentionActivityReader == nil {
		return AttentionActivity{}, []string{"attention activity projection is unavailable"}
	}
	activity, err := a.AttentionActivityReader(ctx, itemID)
	if err != nil {
		return AttentionActivity{}, []string{"could not read attention activity projection: " + err.Error()}
	}
	return activity, []string{}
}

func latestAttentionTime(current *time.Time, at time.Time) *time.Time {
	if current != nil && !current.Before(at) {
		return current
	}
	return timePointer(at)
}

func (a *App) workAgentStatus(status AgentStatusResult) WorkAgentStatus {
	observation := viewapp.Observation{Status: status.Status, Reason: status.Reason, ProcessOnline: status.Process.Online, TurnState: status.Conversation.InferredTurnState, LastActivityAgeSeconds: status.Conversation.LastActivityAgeSeconds}
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
