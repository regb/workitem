package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/dataroot"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	processpkg "github.com/regb/workitem/internal/process"
	"github.com/regb/workitem/internal/runtimepath"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
	"github.com/regb/workitem/internal/xdg"
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
	database          *Database
	process           processpkg.Inspector
	git               gitpkg.Client
	tmux              tmuxpkg.Client
	runtimeSocketRoot string
}

func NewExternalObserver(database *Database, dataRoot string) *ExternalObserver {
	observer := &ExternalObserver{database: database, process: processpkg.New(), git: gitpkg.New("git"), tmux: tmuxpkg.New("tmux")}
	paths, err := xdg.FromEnv()
	key, keyErr := dataroot.Key(dataRoot)
	if err == nil && keyErr == nil {
		observer.runtimeSocketRoot = filepath.Join(paths.RuntimeDir, "wi", key)
	}
	return observer
}

func (o *ExternalObserver) Reconcile(ctx context.Context) error {
	inspection, err := o.Inspect(ctx, true)
	if err != nil {
		return err
	}
	return o.ReconcileWith(ctx, inspection)
}

func (o *ExternalObserver) ReconcileRuntime(ctx context.Context) error {
	inspection, err := o.Inspect(ctx, false)
	if err != nil {
		return err
	}
	return o.ReconcileWith(ctx, inspection)
}

// Inspection holds what the observer saw outside Wi: tmux panes, the process
// table and, optionally, Git status of active checkouts. Taking it only reads
// the database, so callers take it outside the domain lock and apply it with
// ReconcileWith; the lock is then held only to fold it into observations.
type Inspection struct {
	at        time.Time
	panes     map[string][]model.TerminalPaneInfo
	processes processpkg.Inspector
	// checkouts is nil when worktrees were not inspected. Each applies only
	// while its item still claims the same checkout.
	checkouts map[string]inspectedWorktree
}

type inspectedWorktree struct {
	path, branch string
	worktree     *ProjectedWorktree
}

// maxWorktreeInspections bounds concurrent Git processes.
const maxWorktreeInspections = 8

// Inspect probes tmux and processes, and with worktrees runs Git status for
// every active checkout in parallel.
func (o *ExternalObserver) Inspect(ctx context.Context, worktrees bool) (*Inspection, error) {
	result := &Inspection{at: time.Now().UTC(), panes: map[string][]model.TerminalPaneInfo{}}
	if panes, err := o.tmux.ListPanes(ctx); err == nil {
		for _, pane := range panes {
			result.panes[pane.SessionName] = append(result.panes[pane.SessionName], pane)
		}
	}
	// One process table read serves every item of the pass.
	result.processes = o.process.Snapshot()
	if !worktrees {
		return result, nil
	}
	manifests, err := o.database.ListManifests()
	if err != nil {
		return nil, err
	}
	result.checkouts = map[string]inspectedWorktree{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, maxWorktreeInspections)
	for _, manifest := range manifests {
		if manifest.State != model.StateActive {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(manifest model.Manifest) {
			defer wg.Done()
			defer func() { <-slots }()
			path, branch := checkoutKey(manifest)
			worktree := o.observeWorktree(ctx, manifest)
			mu.Lock()
			result.checkouts[manifest.ID] = inspectedWorktree{path: path, branch: branch, worktree: worktree}
			mu.Unlock()
		}(manifest)
	}
	wg.Wait()
	return result, ctx.Err()
}

func checkoutKey(manifest model.Manifest) (path, branch string) {
	if manifest.Checkout.Present() && manifest.Checkout.Claim != nil {
		path = manifest.Checkout.Claim.Path
	}
	return path, manifest.Checkout.Specification.Branch
}

// ReconcileWith observes runtimes and terminals for active items from an
// inspection, keeping the last observed worktree where it inspected none.
func (o *ExternalObserver) ReconcileWith(ctx context.Context, inspection *Inspection) error {
	manifests, err := o.database.ListManifests()
	if err != nil {
		return err
	}
	updates := []ProjectionUpdate{}
	changed := []string{}
	for _, manifest := range manifests {
		if err := ctx.Err(); err != nil {
			return err
		}
		if manifest.State != model.StateActive {
			continue
		}
		observation := o.observe(ctx, manifest, inspection)
		var previous AgentObservation
		if found, _ := o.database.ReadProjection(AgentObservationProjection, manifest.ID, &previous); !found || observationShown(previous) != observationShown(observation) {
			changed = append(changed, manifest.ID)
		}
		encoded, err := json.Marshal(observation)
		if err != nil {
			return err
		}
		updates = append(updates, ProjectionUpdate{Projection: AgentObservationProjection, Key: manifest.ID, Value: encoded})
	}
	if err := o.database.UpdateProjections(updates); err != nil {
		return err
	}
	// Readers waiting for changes learn of what only observation found, such
	// as a runtime that exited or a worktree that became dirty, at once
	// rather than on their fallback beat.
	o.database.changes.record(changed...)
	return nil
}

// observedState is what readers show of an observation; its timestamps
// change on every pass and are left out.
type observedState struct {
	status, reason, turn string
	online               bool
	worktree             ProjectedWorktree
}

func observationShown(o AgentObservation) observedState {
	shown := observedState{status: o.Status, reason: o.Reason, turn: o.TurnState, online: o.ProcessOnline}
	if o.Worktree != nil {
		shown.worktree = *o.Worktree
	}
	return shown
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
	changed := []string{}
	for _, manifest := range manifests {
		if err := ctx.Err(); err != nil {
			return err
		}
		if manifest.State != model.StateActive {
			continue
		}
		observation := AgentObservation{WorkItemID: manifest.ID, Status: "problem", ObservedAt: time.Now().UTC(), Warnings: []string{}, Activity: AttentionActivity{WorkItemID: manifest.ID}}
		found, _ := o.database.ReadProjection(AgentObservationProjection, manifest.ID, &observation)
		previous := observationShown(observation)
		if err := o.applyActionability(manifest.ID, &observation); err != nil {
			return err
		}
		if !found || observationShown(observation) != previous {
			changed = append(changed, manifest.ID)
		}
		encoded, err := json.Marshal(observation)
		if err != nil {
			return err
		}
		updates = append(updates, ProjectionUpdate{Projection: AgentObservationProjection, Key: manifest.ID, Value: encoded})
	}
	if err := o.database.UpdateProjections(updates); err != nil {
		return err
	}
	o.database.changes.record(changed...)
	return nil
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
	case liveOwnerCurrent && live.Source == "daemon.runtime.live" && observation.ProcessOnline && inputBlocked(owner.Readiness, owner.ReadinessReasonCode) && owner.ReadinessObservedAt != nil && (live.LastRequestedAt == nil || !owner.ReadinessObservedAt.Before(*live.LastRequestedAt)) && (live.LastCompletedAt == nil || !owner.ReadinessObservedAt.Before(*live.LastCompletedAt)):
		observation.Status = "problem"
		observation.Reason = driverprotocol.RuntimeReasonText(owner.ReadinessReasonCode)
	case liveOwnerCurrent && live.Source == "daemon.runtime.live" && observation.ProcessOnline && live.AgentState == "active" && awaitingAnswer(owner.Readiness, owner.ReadinessReasonCode) && owner.ReadinessObservedAt != nil && (live.LastRequestedAt == nil || !owner.ReadinessObservedAt.Before(*live.LastRequestedAt)):
		observation.Status = "idle"
		observation.Reason = driverprotocol.RuntimeReasonText(owner.ReadinessReasonCode)
		observation.TurnState = "incomplete"
	case liveOwnerCurrent && live.Source == "daemon.runtime.live" && live.AgentState == "active" && !observation.ProcessOnline:
		observation.Status = "problem"
		observation.Reason = "live agent loop was active but runtime ownership is offline"
	case liveOwnerCurrent && live.Source == "daemon.runtime.live" && live.AgentState == "active" && observation.ProcessOnline && o.runtimeControlUnavailable(itemID, owner):
		// PID liveness does not prove the bridge that reported this run still
		// exists. Do not invent settlement from history or from elapsed time.
		observation.AgentLoopState = ""
		observation.Status = "problem"
		observation.Reason = "runtime control channel is unavailable; live agent activity cannot be confirmed"
	case liveOwnerCurrent && live.Source == "daemon.runtime.live" && live.AgentState == "active" && observation.ProcessOnline:
		observation.Status = "busy"
		observation.Reason = "live agent loop is active"
		observation.TurnState = "incomplete"
		observation.LastActivityAt = live.LastEventAt
	case liveCurrent && live.AgentState == "failed":
		observation.Status = "problem"
		observation.Reason = "live agent loop settled with a failure"
		if reason := driverprotocol.RuntimeReasonText(live.FailureReason); reason != "" {
			observation.Reason = reason
		}
		if live.LeafID != "" {
			observation.Reason = "native agent reports failure on its selected branch"
		}
		observation.TurnState = "failed"
	case liveCurrent && live.AgentState == "idle":
		observation.Status = "idle"
		observation.Reason = "native agent reports idle on its selected branch"
		observation.TurnState = "idle"
		observation.LastActivityAt = live.LastEventAt
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
	case !conversationFound && ownerFound && startingRuntime(owner, observation.ProcessOnline):
		// Nothing is written before the first turn; the agent is coming up.
		observation.Status = "busy"
		observation.Reason = "agent is starting"
		observation.TurnState = "incomplete"
	case !conversationFound && (!ownerFound || owner.State == model.AgentRuntimeStopped):
		observation.Status = "idle"
		observation.Reason = "agent has not started; send it a message to start it"
	case !conversationFound:
		observation.Status = "problem"
		observation.Reason = "daemon has not indexed the conversation"
	case conversation.InferredTurnState == "failed":
		observation.Status = "problem"
		observation.Reason = "latest terminal assistant message failed"
		if last := conversation.LastTerminalAssistant; last != nil && last.StopReason == "usage_limit" {
			observation.Reason = driverprotocol.RuntimeReasonText(last.StopReason)
		}
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

// agentStartGrace bounds how long a runtime may be recorded as starting
// without its process being seen before it counts as a failed start.
const agentStartGrace = 30 * time.Second

// startingRuntime reports a runtime that is coming up: its process is seen,
// or it was recorded moments ago and its host may not run yet.
func startingRuntime(owner model.AgentRuntime, online bool) bool {
	if owner.State != model.AgentRuntimeStarting && owner.State != model.AgentRuntimeRunning {
		return false
	}
	return online || time.Since(owner.UpdatedAt) < agentStartGrace
}

func (o *ExternalObserver) runtimeControlUnavailable(itemID string, owner model.AgentRuntime) bool {
	if o.runtimeSocketRoot == "" || (!owner.Capabilities.Prompt && !owner.Capabilities.Cancel) {
		return false
	}
	path := runtimepath.SocketPath(o.runtimeSocketRoot, "", itemID, owner.ID, false)
	info, err := os.Stat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return true
	}
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return true
	}
	_ = connection.Close()
	return false
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

func (o *ExternalObserver) observe(ctx context.Context, manifest model.Manifest, inspection *Inspection) AgentObservation {
	panes := inspection.panes[manifest.TerminalSessionName()]
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
		info, err := inspection.processes.Info(runtime.HostPID)
		if err != nil && runtime.UpdatedAt.After(inspection.at) {
			// The runtime started after the process table was read.
			info, err = o.process.Info(runtime.HostPID)
		}
		if err == nil && info.State != "Z" && info.StartedAs(runtime.HostStartTime) && (runtime.HostProcessGroup == 0 || info.PGRP == runtime.HostProcessGroup) {
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
			if backend, found, err := inspection.processes.FindDescendant(pane.PanePID, []string{"internal-driver", "wi-agent-"}); err == nil && found {
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
	path, branch := checkoutKey(manifest)
	if inspected, ok := inspection.worktree(manifest.ID); ok && inspected.path == path && inspected.branch == branch {
		observation.Worktree = inspected.worktree
		observation.WorktreeObservedAt = inspection.at
	} else {
		var previous AgentObservation
		if found, _ := o.database.ReadProjection(AgentObservationProjection, manifest.ID, &previous); found {
			observation.Worktree = previous.Worktree
			observation.WorktreeObservedAt = previous.WorktreeObservedAt
		}
	}
	return observation
}

func (i *Inspection) worktree(itemID string) (inspectedWorktree, bool) {
	inspected, ok := i.checkouts[itemID]
	return inspected, ok
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
