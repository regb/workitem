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
	result, err := a.primaryAgentService().AgentStatus(ctx, agentcore.AgentStatusOptions{ResolveOptions: opts.ResolveOptions, StaleAfter: opts.StaleAfter})
	if err != nil {
		return AgentStatusResult{}, err
	}
	if manifest.Checkout.Specification.Executor == "" || manifest.Checkout.Specification.Executor == model.ExecutorLocal {
		return result, nil
	}
	client, err := a.executorClient(manifest.Checkout.Specification.Executor)
	if err != nil {
		return AgentStatusResult{}, err
	}
	workspace, workspaceErr := client.WorkspaceStatus(ctx, manifest.ID)
	if workspaceErr != nil {
		result.Warnings = append(result.Warnings, "could not inspect remote workspace: "+workspaceErr.Error())
		return result, nil
	}
	var status WorkspaceStatusResult
	if err := json.Unmarshal(workspace.StatusJSON, &status); err != nil {
		result.Warnings = append(result.Warnings, "could not decode remote workspace status: "+err.Error())
		return result, nil
	}
	state, reason := "clean", "remote checkout is clean"
	if status.Git.BranchMismatch {
		state, reason = "problem", "remote checkout branch differs from expected branch"
	} else if status.Git.Dirty || status.Git.CurrentHead != "" && status.Git.CurrentHead != manifest.Repository.CreatedFromCommit {
		state, reason = "changed", "remote checkout has changes"
	}
	checkoutPath := ""
	if manifest.Checkout.Claim != nil {
		checkoutPath = manifest.Checkout.Claim.Path
	}
	result.Worktree = &WorktreeStatus{Status: state, Reason: reason, CheckoutPath: checkoutPath, Head: status.Git.CurrentHead, ExpectedBranch: status.Git.ExpectedBranch, CurrentBranch: status.Git.CurrentBranch, BranchMatches: status.Git.BranchMatches, BranchMismatch: status.Git.BranchMismatch, Dirty: status.Git.Dirty, HasChanges: state == "changed"}
	return result, nil
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
