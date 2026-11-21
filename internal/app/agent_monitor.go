package app

import (
	"context"
	"time"

	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/model"
)

type AgentStatusOptions struct {
	ResolveOptions
	StaleAfter time.Duration
}
type AgentStatusResult = agentcore.AgentStatusResult
type AgentProcessStatus = agentcore.AgentProcessStatus
type ConversationStatus = agentcore.ConversationStatus
type ConversationEventSummary = agentcore.ConversationEventSummary
type WorktreeStatus = agentcore.WorktreeStatus

// AgentStatus inspects an item's agent: process, conversation and checkout.
// Its status comes from the daemon's observation when there is one, which
// folds live runtime events, so every caller sees the status the API
// reports; deriving it again from the conversation alone disagreed with it.
// Without a daemon, as in tests, it is derived from the conversation.
func (a *App) AgentStatus(ctx context.Context, opts AgentStatusOptions) (AgentStatusResult, error) {
	res, err := a.primaryAgentService().AgentStatus(ctx, agentcore.AgentStatusOptions{ResolveOptions: opts.ResolveOptions, StaleAfter: opts.StaleAfter})
	if err != nil || a.WorkListObserver == nil || res.WorkItemID == "" {
		return res, err
	}
	if observation, err := a.WorkListObserver(ctx, res.WorkItemID); err == nil && observation.Status != "" {
		res.Status, res.Reason = observation.Status, observation.Reason
	}
	return res, nil
}
func (a *App) recordPrimaryTerminalRuntime(ctx context.Context, m model.Manifest, window string) []string {
	return a.primaryAgentService().RecordPrimaryTerminalRuntime(ctx, m, window)
}
func (a *App) inspectConversation(ctx context.Context, m model.Manifest, now time.Time) (ConversationStatus, []string) {
	if a.ConversationProjectionObserver != nil {
		return a.ConversationProjectionObserver(ctx, m, now)
	}
	return a.observeConversation(ctx, m, now)
}
