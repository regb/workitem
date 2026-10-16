package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	processpkg "github.com/regb/workitem/internal/process"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
)

const AgentObservationProjection = "agent_observations"

type ProjectedWorktree struct {
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	CheckoutPath   string `json:"checkout_path,omitempty"`
	Head           string `json:"head,omitempty"`
	ExpectedBranch string `json:"expected_branch,omitempty"`
	CurrentBranch  string `json:"current_branch,omitempty"`
	BranchMatches  bool   `json:"branch_matches"`
	BranchMismatch bool   `json:"branch_mismatch,omitempty"`
	Dirty          bool   `json:"dirty"`
	HasChanges     bool   `json:"has_changes"`
}

type ProjectedProcess struct {
	RuntimeID       string `json:"runtime_id,omitempty"`
	RuntimeMode     string `json:"runtime_mode,omitempty"`
	RuntimeHostPID  int    `json:"runtime_host_pid,omitempty"`
	TmuxSession     string `json:"tmux_session,omitempty"`
	TmuxWindow      string `json:"tmux_window,omitempty"`
	TmuxPaneID      string `json:"tmux_pane_id,omitempty"`
	TmuxPaneIndex   string `json:"tmux_pane_index,omitempty"`
	TmuxPanePID     int    `json:"tmux_pane_pid,omitempty"`
	TmuxPaneCommand string `json:"tmux_pane_command,omitempty"`
	TmuxPanePath    string `json:"tmux_pane_path,omitempty"`
	BackendPID      int    `json:"backend_pid,omitempty"`
	DiscoverySource string `json:"discovery_source,omitempty"`
}

type AgentObservation struct {
	WorkItemID         string                 `json:"work_item_id"`
	Status             string                 `json:"status"`
	Reason             string                 `json:"reason,omitempty"`
	ProcessOnline      bool                   `json:"process_online"`
	Process            ProjectedProcess       `json:"process"`
	Runtime            *model.AgentRuntime    `json:"runtime,omitempty"`
	Terminal           *model.TerminalRuntime `json:"terminal,omitempty"`
	AgentLoopState     string                 `json:"agent_loop_state,omitempty"`
	TurnState          string                 `json:"turn_state,omitempty"`
	LastActivityAt     *time.Time             `json:"last_activity_at,omitempty"`
	Worktree           *ProjectedWorktree     `json:"worktree,omitempty"`
	WorktreeObservedAt time.Time              `json:"worktree_observed_at,omitempty"`
	Activity           AttentionActivity      `json:"activity"`
	ObservedAt         time.Time              `json:"observed_at"`
	Warnings           []string               `json:"warnings,omitempty"`
}

type AgentObservationResult struct {
	Projection  ProjectionMetadata `json:"projection"`
	Found       bool               `json:"found"`
	Observation AgentObservation   `json:"observation"`
}

type ExternalObserver struct {
	database *Database
	process  processpkg.Inspector
	git      gitpkg.Client
	tmux     tmuxpkg.Client
}

func NewExternalObserver(database *Database, _ string) *ExternalObserver {
	return &ExternalObserver{database: database, process: processpkg.New(), git: gitpkg.New("git"), tmux: tmuxpkg.New("tmux")}
}

func (o *ExternalObserver) Reconcile(ctx context.Context) error {
	return o.reconcile(ctx, true)
}

func (o *ExternalObserver) ReconcileRuntime(ctx context.Context) error {
	return o.reconcile(ctx, false)
}

func (o *ExternalObserver) reconcile(ctx context.Context, includeWorktree bool) error {
	manifests, err := o.database.ListManifests()
	if err != nil {
		return err
	}
	panesBySession := map[string][]model.TerminalPaneInfo{}
	if panes, paneErr := o.tmux.ListPanes(ctx); paneErr == nil {
		for _, pane := range panes {
			panesBySession[pane.SessionName] = append(panesBySession[pane.SessionName], pane)
		}
	}
	updates := []ProjectionUpdate{}
	for _, manifest := range manifests {
		if err := ctx.Err(); err != nil {
			return err
		}
		if manifest.State != model.StateActive {
			continue
		}
		observation := o.observe(ctx, manifest, panesBySession[manifest.TerminalSessionName()], includeWorktree)
		encoded, err := json.Marshal(observation)
		if err != nil {
			return err
		}
		updates = append(updates, ProjectionUpdate{Projection: AgentObservationProjection, Key: manifest.ID, Value: encoded})
	}
	return o.database.UpdateProjections(updates)
}

// ReconcileActionability folds the latest native conversation checkpoint and live runtime
// activity into existing observations without re-running process, tmux, or Git
// inspection. It is used as the picker pre-snapshot barrier.
func (o *ExternalObserver) ReconcileActionability(ctx context.Context) error {
	manifests, err := o.database.ListManifests()
	if err != nil {
		return err
	}
	updates := []ProjectionUpdate{}
	for _, manifest := range manifests {
		if err := ctx.Err(); err != nil {
			return err
		}
		if manifest.State != model.StateActive {
			continue
		}
		observation := AgentObservation{WorkItemID: manifest.ID, Status: "problem", ObservedAt: time.Now().UTC(), Warnings: []string{}, Activity: AttentionActivity{WorkItemID: manifest.ID}}
		_, _ = o.database.ReadProjection(AgentObservationProjection, manifest.ID, &observation)
		if err := o.applyActionability(manifest.ID, &observation); err != nil {
			return err
		}
		encoded, err := json.Marshal(observation)
		if err != nil {
			return err
		}
		updates = append(updates, ProjectionUpdate{Projection: AgentObservationProjection, Key: manifest.ID, Value: encoded})
	}
	return o.database.UpdateProjections(updates)
}

func (o *ExternalObserver) applyActionability(itemID string, observation *AgentObservation) error {
	var owner model.AgentRuntime
	ownerFound, err := o.database.ReadProjection(RuntimeOwnershipProjection, itemID, &owner)
	if err != nil {
		return fmt.Errorf("read runtime ownership projection: %w", err)
	}
	if ownerFound {
		ownerCopy := owner
		observation.Runtime = &ownerCopy
	} else {
		observation.Runtime = nil
	}

	var conversation ConversationIndex
	conversationFound, err := o.database.ReadProjection(ConversationProjection, itemID, &conversation)
	if err != nil {
		return fmt.Errorf("read conversation projection: %w", err)
	}
	activity := AttentionActivity{WorkItemID: itemID}
	if _, err := o.database.ReadProjection(AttentionActivityProjection, itemID, &activity); err != nil {
		return fmt.Errorf("read attention projection: %w", err)
	}
	activity.LastRequestedAt = laterTime(activity.LastRequestedAt, observation.Activity.LastRequestedAt)
	activity.LastCompletedAt = laterTime(activity.LastCompletedAt, observation.Activity.LastCompletedAt)
	activity.LastDeferredAt = laterTime(activity.LastDeferredAt, observation.Activity.LastDeferredAt)
	observation.Activity = activity
	if conversationFound {
		observation.TurnState = conversation.InferredTurnState
		if conversation.LastTurnActivity != nil && !conversation.LastTurnActivity.Timestamp.IsZero() {
			at := conversation.LastTurnActivity.Timestamp.UTC()
			observation.LastActivityAt = &at
		}
	}
	var live RuntimeActivity
	liveFound, err := o.database.ReadProjection(RuntimeActivityProjection, itemID, &live)
	if err != nil {
		return fmt.Errorf("read runtime activity projection: %w", err)
	}
	claimCurrent := true
	if ownerFound && owner.WorkspaceClaimID != "" {
		canonical, err := o.database.CanonicalManifest(itemID)
		if err != nil {
			return err
		}
		claim := canonical.Manifest.Checkout.Claim
		claimCurrent = claim != nil && claim.ID == owner.WorkspaceClaimID && claim.Generation == owner.WorkspaceClaimGeneration
		if observation.ProcessOnline && !claimCurrent {
			observation.Status = "problem"
			observation.Reason = "active agent runtime belongs to a superseded workspace claim"
			return nil
		}
	}
	if liveFound && claimCurrent {
		observation.Activity.LastRequestedAt = laterTime(observation.Activity.LastRequestedAt, live.LastRequestedAt)
		observation.Activity.LastCompletedAt = laterTime(observation.Activity.LastCompletedAt, live.LastCompletedAt)
	}
	observation.AgentLoopState = ""
	liveOwnerCurrent := liveFound && ownerFound && claimCurrent && live.RuntimeID == owner.ID
	liveCurrent := liveOwnerCurrent && live.Source == "daemon.runtime.live" && live.LastEventAt != nil && (!conversationFound || conversation.LastTurnActivity == nil || !live.LastEventAt.Before(conversation.LastTurnActivity.Timestamp))
	if liveOwnerCurrent && live.Source == "daemon.runtime.live" {
		observation.AgentLoopState = live.AgentState
	}
	switch {
	case liveOwnerCurrent && live.RuntimeState == model.AgentRuntimeProblem:
		observation.Status = "problem"
		observation.Reason = "live runtime event reports a failure"
	case liveOwnerCurrent && live.Source == "daemon.runtime.live" && observation.ProcessOnline && (owner.Readiness == "blocked" || owner.Readiness == "waiting_input") && owner.ReadinessObservedAt != nil && (live.LastRequestedAt == nil || !owner.ReadinessObservedAt.Before(*live.LastRequestedAt)) && (live.LastCompletedAt == nil || !owner.ReadinessObservedAt.Before(*live.LastCompletedAt)):
		observation.Status = "problem"
		observation.Reason = driverprotocol.RuntimeReasonText(owner.ReadinessReasonCode)
	case liveOwnerCurrent && live.Source == "daemon.runtime.live" && live.AgentState == "active" && !observation.ProcessOnline:
		observation.Status = "problem"
		observation.Reason = "live agent loop was active but runtime ownership is offline"
	case liveOwnerCurrent && live.Source == "daemon.runtime.live" && live.AgentState == "active" && observation.ProcessOnline:
		observation.Status = "busy"
		observation.Reason = "live agent loop is active"
		observation.TurnState = "incomplete"
		observation.LastActivityAt = live.LastEventAt
	case liveCurrent && live.AgentState == "failed":
		observation.Status = "problem"
		observation.Reason = "live agent loop settled with a failure"
		observation.TurnState = "failed"
	case liveCurrent && live.AgentState == "settled":
		observation.Status = "idle"
		observation.Reason = "live agent loop reports settled work"
		observation.TurnState = "idle"
		observation.LastActivityAt = live.LastEventAt
	case liveCurrent && live.TurnState == "busy" && observation.ProcessOnline:
		observation.Status = "busy"
		observation.Reason = "live runtime event reports active work"
		observation.TurnState = "incomplete"
		observation.LastActivityAt = live.LastEventAt
	case liveCurrent && live.TurnState == "idle" && (!conversationFound || conversation.InferredTurnState != "failed"):
		observation.Status = "idle"
		observation.Reason = "live runtime event reports settled work"
		observation.TurnState = "idle"
		observation.LastActivityAt = live.LastEventAt
	case !conversationFound:
		observation.Status = "problem"
		observation.Reason = "daemon has not indexed the conversation"
	case conversation.InferredTurnState == "failed":
		observation.Status = "problem"
		observation.Reason = "latest terminal assistant message failed"
	case conversation.InferredTurnState == "idle":
		observation.Status = "idle"
		observation.Reason = "latest assistant answer is terminal"
	case observation.ProcessOnline:
		observation.Status = "busy"
		observation.Reason = "latest conversation activity is incomplete and runtime ownership is online"
	default:
		observation.Status = "problem"
		observation.Reason = "latest conversation activity is incomplete and runtime ownership is offline"
	}
	return nil
}

func laterTime(left, right *time.Time) *time.Time {
	if left == nil {
		return right
	}
	if right == nil || !right.After(*left) {
		return left
	}
	return right
}

func (o *ExternalObserver) observe(ctx context.Context, manifest model.Manifest, panes []model.TerminalPaneInfo, includeWorktree bool) AgentObservation {
	now := time.Now().UTC()
	observation := AgentObservation{WorkItemID: manifest.ID, Status: "problem", ObservedAt: now, Warnings: []string{}, Activity: AttentionActivity{WorkItemID: manifest.ID}}
	var runtimeValue model.AgentRuntime
	runtimeFound, runtimeErr := o.database.ReadProjection(RuntimeOwnershipProjection, manifest.ID, &runtimeValue)
	var runtime *model.AgentRuntime
	if runtimeFound {
		runtime = &runtimeValue
		observation.Runtime = runtime
	}
	if runtimeErr != nil {
		observation.Warnings = append(observation.Warnings, "read runtime state: "+runtimeErr.Error())
	}
	var terminalValue model.TerminalRuntime
	terminalFound, terminalErr := o.database.ReadProjection(TerminalOwnershipProjection, manifest.ID, &terminalValue)
	var terminal *model.TerminalRuntime
	if terminalFound {
		terminal = &terminalValue
		observation.Terminal = terminal
	}
	if terminalErr != nil {
		observation.Warnings = append(observation.Warnings, "read terminal state: "+terminalErr.Error())
	}
	if runtime != nil {
		observation.Process.RuntimeID = runtime.ID
		observation.Process.RuntimeMode = runtime.Mode
		observation.Process.RuntimeHostPID = runtime.HostPID
		if runtime.Mode == "tui" {
			observation.Process.TmuxSession = manifest.TerminalSessionName()
			observation.Process.TmuxWindow = "agent"
		}
	}
	if runtime != nil && runtime.HostPID > 0 && runtime.HostStartTime > 0 {
		if info, err := o.process.Info(runtime.HostPID); err == nil && info.State != "Z" && info.StartTime == runtime.HostStartTime && (runtime.HostProcessGroup == 0 || info.PGRP == runtime.HostProcessGroup) {
			observation.ProcessOnline = true
			observation.Process.DiscoverySource = "runtime"
		}
	}
	if !observation.ProcessOnline {
		window := "agent"
		if terminal != nil && strings.TrimSpace(terminal.TmuxWindow) != "" {
			window = terminal.TmuxWindow
		}
		for _, pane := range panes {
			if pane.WindowName != window || pane.PanePID <= 0 {
				continue
			}
			observation.Process.TmuxSession = pane.SessionName
			observation.Process.TmuxWindow = pane.WindowName
			observation.Process.TmuxPaneID = pane.PaneID
			observation.Process.TmuxPaneIndex = pane.PaneIndex
			observation.Process.TmuxPanePID = pane.PanePID
			observation.Process.TmuxPaneCommand = pane.Command
			if backend, found, err := o.process.FindDescendant(pane.PanePID, []string{"internal-driver", "wi-agent-"}); err == nil && found {
				observation.ProcessOnline = true
				observation.Process.BackendPID = backend.PID
				observation.Process.DiscoverySource = "tmux"
			}
			break
		}
	}
	if err := o.applyActionability(manifest.ID, &observation); err != nil {
		observation.Warnings = append(observation.Warnings, err.Error())
	}
	if includeWorktree {
		observation.Worktree = o.observeWorktree(ctx, manifest)
		observation.WorktreeObservedAt = now
	} else {
		var previous AgentObservation
		if found, _ := o.database.ReadProjection(AgentObservationProjection, manifest.ID, &previous); found {
			observation.Worktree = previous.Worktree
			observation.WorktreeObservedAt = previous.WorktreeObservedAt
		}
	}
	return observation
}

func (o *ExternalObserver) observeWorktree(ctx context.Context, manifest model.Manifest) *ProjectedWorktree {
	expected := strings.TrimSpace(manifest.Checkout.Specification.Branch)
	result := &ProjectedWorktree{Status: "absent", Reason: "checkout is absent", ExpectedBranch: expected}
	if !manifest.Checkout.Present() || manifest.Checkout.Claim == nil || strings.TrimSpace(manifest.Checkout.Claim.Path) == "" {
		return result
	}
	result.CheckoutPath = manifest.Checkout.Claim.Path
	if _, err := os.Stat(result.CheckoutPath); err != nil {
		result.Status = "problem"
		result.Reason = "checkout path is unavailable: " + err.Error()
		return result
	}
	head, branch, status, err := o.git.WorktreeSnapshot(ctx, result.CheckoutPath)
	if err != nil {
		result.Status = "problem"
		result.Reason = "could not inspect checkout: " + err.Error()
		return result
	}
	result.Head = head
	result.CurrentBranch = branch
	result.BranchMatches = expected != "" && branch == expected
	result.BranchMismatch = expected != "" && branch != expected
	result.Dirty = strings.TrimSpace(status) != ""
	result.HasChanges = result.BranchMismatch || result.Dirty || head != "" && manifest.Repository.CreatedFromCommit != "" && head != manifest.Repository.CreatedFromCommit
	switch {
	case result.BranchMismatch:
		result.Status = "changed"
		result.Reason = fmt.Sprintf("checkout branch %s differs from expected %s", branch, expected)
	case result.Dirty:
		result.Status = "changed"
		result.Reason = "checkout has uncommitted changes"
	case result.HasChanges:
		result.Status = "changed"
		result.Reason = "checkout HEAD differs from the item's created-from commit"
	default:
		result.Status = "clean"
		result.Reason = "checkout matches the created-from commit"
		if expected != "" {
			result.Reason = "checkout matches the expected branch and created-from commit"
		}
	}
	return result
}
