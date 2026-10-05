package app

import (
	"context"
	"encoding/json"
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

func (a *App) AgentStatus(ctx context.Context, opts AgentStatusOptions) (AgentStatusResult, error) {
	manifest, err := a.ResolveItem(ctx, opts.ResolveOptions)
	if err != nil {
		return AgentStatusResult{}, err
	}
	if manifest.Checkout.Specification.Executor != "" && manifest.Checkout.Specification.Executor != model.ExecutorLocal {
		client, err := a.executorClient(manifest.Checkout.Specification.Executor)
		if err != nil {
			return AgentStatusResult{}, err
		}
		remote, err := client.RuntimeStatus(ctx, manifest.ID)
		if err != nil {
			return AgentStatusResult{}, err
		}
		var result AgentStatusResult
		if err := json.Unmarshal(remote.StatusJSON, &result); err != nil {
			return AgentStatusResult{}, err
		}
		result.Manifest = manifest
		if result.Runtime != nil {
			result.Runtime.Executor = manifest.Checkout.Specification.Executor
			result.Runtime.HostPID, result.Runtime.HostProcessGroup, result.Runtime.HostStartTime = 0, 0, 0
			_ = a.Store.SaveAgentRuntime(ctx, manifest.ID, *result.Runtime)
		}
		result.Warnings = append(result.Warnings, a.ingestExecutorEvents(ctx, remote.EventsJSON)...)
		if err := a.checkpointExecutorConversation(ctx, manifest, remote.Conversation); err != nil {
			result.Warnings = append(result.Warnings, "could not checkpoint executor conversation: "+err.Error())
		}
		if status, reason := executorEventStatus(remote.EventsJSON); status != "" && result.Conversation.UnavailableReason != "" {
			result.Status, result.Reason = status, reason
		}
		checkoutPath := ""
		if manifest.Checkout.Claim != nil {
			checkoutPath = manifest.Checkout.Claim.Path
		}
		if result.Worktree != nil {
			result.Worktree.CheckoutPath = checkoutPath
		} else if workspace, workspaceErr := client.WorkspaceStatus(ctx, manifest.ID); workspaceErr == nil {
			var status WorkspaceStatusResult
			if json.Unmarshal(workspace.StatusJSON, &status) == nil {
				state := "clean"
				reason := "remote checkout is clean"
				if status.Git.BranchMismatch {
					state, reason = "problem", "remote checkout branch differs from expected branch"
				} else if status.Git.Dirty || status.Git.CurrentHead != "" && status.Git.CurrentHead != manifest.Repository.CreatedFromCommit {
					state, reason = "changed", "remote checkout has changes"
				}
				result.Worktree = &WorktreeStatus{Status: state, Reason: reason, CheckoutPath: checkoutPath, Head: status.Git.CurrentHead, ExpectedBranch: status.Git.ExpectedBranch, CurrentBranch: status.Git.CurrentBranch, BranchMatches: status.Git.BranchMatches, BranchMismatch: status.Git.BranchMismatch, Dirty: status.Git.Dirty, HasChanges: state == "changed"}
			}
		}
		return result, nil
	}
	return a.primaryAgentService().AgentStatus(ctx, agentcore.AgentStatusOptions{ResolveOptions: opts.ResolveOptions, StaleAfter: opts.StaleAfter})
}
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
