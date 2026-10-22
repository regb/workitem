package app

import (
	"context"
	"errors"
	"fmt"
	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"strings"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app/contract"
	itemlock "github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

// lockController serializes handoff, pane repair, runtime replacement and
// structured delivery across CLI processes. Always take it before item locks.
func (a *App) lockController(ctx context.Context, id string) (func(), error) {
	lock, err := itemlock.AcquireOperation(ctx, a.Store.LockPath(id)+".controller")
	if err != nil {
		return func() {}, err
	}
	return func() { _ = lock.Release() }, nil
}

// WebLeaseTTL bounds how long a browser keeps control without renewing it.
const WebLeaseTTL = 45 * time.Second

// ControlLeases is the coordinator's in-memory controller lease table.
type ControlLeases interface {
	Lease(ctx context.Context, itemID string) (*model.ControlLease, error)
	AcquireLease(ctx context.Context, request model.LeaseRequest) (model.LeaseResult, error)
	ReleaseLease(ctx context.Context, request model.LeaseRequest) (model.LeaseResult, error)
}

// paneControl is the optional tmux capability used to make terminal clients
// read-only while another client controls a TUI runtime.
type paneControl interface {
	SetPaneInput(ctx context.Context, target string, enabled bool) error
	PaneInputDisabled(ctx context.Context, target string) (bool, error)
	SessionClients(ctx context.Context, session string) ([]string, error)
	DisplayMessage(ctx context.Context, target, message string) error
}

// ControlStatus describes who may send input to an item's runtime and what
// the runtime can accept. Holder is the effective controller: an explicit
// live lease, or an attached terminal client when nobody holds a lease on a
// TUI runtime.
type ControlStatus struct {
	WorkItemID      string              `json:"work_item_id"`
	Lease           *model.ControlLease `json:"lease,omitempty"`
	Holder          string              `json:"holder,omitempty"`
	Implicit        bool                `json:"implicit,omitempty"`
	TerminalClients int                 `json:"terminal_clients"`
	RuntimeOnline   bool                `json:"runtime_online"`
	RuntimeMode     string              `json:"runtime_mode,omitempty"`
	RuntimeID       string              `json:"runtime_id,omitempty"`
	Channels        model.Channels      `json:"channels"`
	Busy            bool                `json:"busy"`
	storedLease     *model.ControlLease
}

// ControlPlan is what taking control would do. Anything that interrupts
// another client or an agent turn requires confirmation.
type ControlPlan struct {
	Holder            string              `json:"holder"`
	Displaces         *model.ControlLease `json:"displaces,omitempty"`
	DisplacesTerminal bool                `json:"displaces_terminal,omitempty"`
	Abort             bool                `json:"abort,omitempty"`
	Replace           string              `json:"replace,omitempty"`
	Start             string              `json:"start,omitempty"`
	NeedsConfirmation bool                `json:"needs_confirmation"`
	Reasons           []string            `json:"reasons"`
	// Unavailable explains why the holder cannot get the channel it needs:
	// no runtime mode of the agent profile offers it. Taking control fails.
	Unavailable string `json:"unavailable,omitempty"`
}

type ControlRequest struct {
	ResolveOptions
	Holder   string
	ClientID string
	Label    string
	// Confirm accepts a plan that displaces another controller, aborts the
	// current turn, or restarts the runtime in another mode.
	Confirm bool
	// EnsureRuntime starts or replaces the runtime so it exposes the channel
	// this holder needs. Terminal callers leave it false and start the TUI
	// themselves while entering the terminal.
	EnsureRuntime bool
	TTL           time.Duration
}

type ControlResult struct {
	WorkItemID string              `json:"work_item_id"`
	Granted    bool                `json:"granted"`
	Lease      *model.ControlLease `json:"lease,omitempty"`
	Plan       ControlPlan         `json:"plan"`
	Runtime    *model.AgentRuntime `json:"runtime,omitempty"`
	Warnings   []string            `json:"warnings"`
}

// ControlConfirmationError reports a plan that needs explicit takeover.
type ControlConfirmationError struct {
	Plan ControlPlan
}

func (e *ControlConfirmationError) Error() string {
	return "taking control requires confirmation: " + strings.Join(e.Plan.Reasons, "; ")
}

func agentPaneTarget(m model.Manifest) string {
	return m.TerminalSessionName() + ":agent"
}

func (a *App) paneControl() paneControl {
	if control, ok := a.Tmux.(paneControl); ok {
		return control
	}
	return nil
}

// ControlStatus reads the controller lease and runtime channels. When no
// lease is live it re-enables terminal input that an expired web lease left
// disabled, so a closed browser never strands a terminal user.
func (a *App) ControlStatus(ctx context.Context, opts ResolveOptions) (ControlStatus, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return ControlStatus{}, err
	}
	unlock, err := a.lockController(ctx, m.ID)
	if err != nil {
		return ControlStatus{}, err
	}
	defer unlock()
	return a.controlStatus(ctx, m, true)
}

func (a *App) controlStatus(ctx context.Context, m model.Manifest, repair bool) (ControlStatus, error) {
	status := ControlStatus{WorkItemID: m.ID}
	if a.Leases != nil {
		lease, err := a.Leases.Lease(ctx, m.ID)
		if err != nil {
			return status, fmt.Errorf("read control lease: %w", err)
		}
		status.Lease = lease
		status.storedLease = lease
	}
	runtime, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil {
		return status, err
	}
	ownership := a.primaryAgentService().ObserveOwnership(runtime)
	status.RuntimeOnline = ownership.ProcessAlive && runtime != nil
	if status.RuntimeOnline {
		status.RuntimeMode = runtime.Mode
		status.RuntimeID = runtime.ID
		status.Channels = runtime.Capabilities.Channels()
		status.Busy = a.AgentBusy(ctx, m.ID)
	} else {
		status.Channels = model.Channels{Observe: true}
	}
	panes := a.paneControl()
	tuiOnline := status.RuntimeOnline && status.RuntimeMode == string(agent.ModeTUI)
	if panes != nil && tuiOnline {
		clients, err := panes.SessionClients(ctx, m.TerminalSessionName())
		if err != nil {
			return status, fmt.Errorf("inspect terminal controller: %w", err)
		}
		status.TerminalClients = len(clients)
	}
	switch {
	case status.Lease != nil && status.Lease.Holder == model.LeaseHolderTerminal && status.TerminalClients == 0:
		// The terminal that took control has detached; treat control as vacant.
		status.Lease = nil
	case status.Lease != nil:
		status.Holder = status.Lease.Holder
	case tuiOnline && status.TerminalClients > 0:
		status.Holder, status.Implicit = model.LeaseHolderTerminal, true
	}
	if status.Lease == nil && repair && panes != nil && tuiOnline {
		if disabled, err := panes.PaneInputDisabled(ctx, agentPaneTarget(m)); err == nil && disabled {
			_ = panes.SetPaneInput(ctx, agentPaneTarget(m), true)
		}
	}
	return status, nil
}

// AgentBusy reports whether the agent may be running a turn. It prefers the
// coordinator observation, which folds live runtime events, and treats an
// unknown status as busy so that takeover never aborts a turn unconfirmed.
func (a *App) AgentBusy(ctx context.Context, itemID string) bool {
	if a.WorkListObserver != nil {
		observation, err := a.WorkListObserver(ctx, itemID)
		return err != nil || observation.Status == "busy" || observation.Status == ""
	}
	status, err := a.AgentStatus(ctx, AgentStatusOptions{ResolveOptions: ResolveOptions{Selector: itemID}})
	return err != nil || status.Status == "busy"
}

func (a *App) planControl(status ControlStatus, request ControlRequest, modes driverprotocol.RuntimeModes) ControlPlan {
	plan := ControlPlan{Holder: request.Holder, Reasons: []string{}}
	switch {
	case status.Lease != nil && !status.Lease.SameClient(request.Holder, request.ClientID):
		lease := *status.Lease
		plan.Displaces = &lease
		plan.Reasons = append(plan.Reasons, "another "+lease.Holder+" client controls this agent")
	case status.Lease == nil && status.Implicit && request.Holder != model.LeaseHolderTerminal:
		plan.DisplacesTerminal = true
		plan.Reasons = append(plan.Reasons, "a terminal is attached to this agent and will become read-only")
	}
	// The web needs structured control, a terminal needs a TUI. Keep a
	// running runtime that has the channel; otherwise use a mode of the same
	// profile that offers it, as the driver reports.
	hasChannel := status.Channels.Control == model.ControlStructured
	missing := "no runtime mode of this agent accepts structured input; control it from a terminal"
	if request.Holder == model.LeaseHolderTerminal {
		hasChannel = status.Channels.Terminal
		missing = "no runtime mode of this agent offers a terminal; control it from the browser"
	}
	mode, available := controlMode(modes, request.Holder)
	switch {
	case !status.RuntimeOnline:
		if request.EnsureRuntime && available {
			plan.Start = string(mode)
		} else if request.EnsureRuntime {
			plan.Unavailable = missing
		}
	case !hasChannel && available:
		plan.Replace = string(mode)
		plan.Reasons = append(plan.Reasons, fmt.Sprintf("the %s runtime will restart in %s mode on the same conversation", status.RuntimeMode, mode))
	case !hasChannel:
		plan.Unavailable = missing
	}
	if status.Busy && (plan.Displaces != nil || plan.DisplacesTerminal || plan.Replace != "") {
		plan.Abort = true
		plan.Reasons = append(plan.Reasons, "the current agent turn will be aborted")
	}
	// Taking a vacant lease on an idle runtime should feel like there is no
	// lease at all, even when the runtime has to change mode.
	plan.NeedsConfirmation = plan.Displaces != nil || plan.DisplacesTerminal || plan.Abort
	return plan
}

// PlanControl reports the control status and what AcquireControl would do.
// Like ControlStatus, it re-enables terminal input left disabled by an
// expired lease.
func (a *App) PlanControl(ctx context.Context, request ControlRequest) (ControlStatus, ControlPlan, error) {
	m, err := a.ResolveItem(ctx, request.ResolveOptions)
	if err != nil {
		return ControlStatus{}, ControlPlan{}, err
	}
	unlock, err := a.lockController(ctx, m.ID)
	if err != nil {
		return ControlStatus{}, ControlPlan{}, err
	}
	defer unlock()
	status, err := a.controlStatus(ctx, m, true)
	if err != nil {
		return status, ControlPlan{}, err
	}
	return status, a.planControl(status, request, a.AgentRuntimeModes(ctx, m.Agent)), nil
}

// AcquireControl makes request.Holder the item's single controller. It
// renews a lease the client already holds, takes a vacant one silently, and
// otherwise requires Confirm. When the runtime lacks the channel the holder
// needs, it aborts any turn, stops the runtime, and (with EnsureRuntime)
// resumes the same conversation in a mode that has it.
func (a *App) AcquireControl(ctx context.Context, request ControlRequest) (ControlResult, error) {
	if a.Leases == nil {
		return ControlResult{}, errors.New("control leases are unavailable without the coordinator")
	}
	m, err := a.ResolveItem(ctx, request.ResolveOptions)
	if err != nil {
		return ControlResult{}, err
	}
	unlock, err := a.lockController(ctx, m.ID)
	if err != nil {
		return ControlResult{}, err
	}
	defer unlock()
	if m.State != model.StateActive {
		return ControlResult{}, fmt.Errorf("work item %s is %s; only active items have a controllable agent", m.ID, m.State)
	}
	status, err := a.controlStatus(ctx, m, false)
	if err != nil {
		return ControlResult{}, err
	}
	plan := a.planControl(status, request, a.AgentRuntimeModes(ctx, m.Agent))
	result := ControlResult{WorkItemID: m.ID, Plan: plan, Warnings: []string{}}
	if plan.Unavailable != "" {
		return result, errors.New(plan.Unavailable)
	}
	if plan.NeedsConfirmation && !request.Confirm {
		return result, &ControlConfirmationError{Plan: plan}
	}
	if plan.Replace != "" || plan.Start != "" {
		if err := a.ValidateAgentConfig(ctx, m.Agent); err != nil {
			return result, err
		}
	}
	if request.Holder == model.LeaseHolderWeb && request.TTL == 0 {
		request.TTL = WebLeaseTTL
	}
	ttl := 0
	if request.TTL > 0 {
		ttl = int(request.TTL / time.Second)
	}
	expected := ""
	if status.storedLease != nil {
		expected = status.storedLease.ID
	}
	takeover := plan.Displaces != nil || status.Lease == nil && status.storedLease != nil
	granted, err := a.Leases.AcquireLease(ctx, model.LeaseRequest{ItemID: m.ID, Holder: request.Holder, ClientID: request.ClientID, Label: request.Label, Takeover: takeover, ExpectedLeaseID: &expected, TTLSeconds: ttl})
	if err != nil {
		return result, fmt.Errorf("acquire control lease: %w", err)
	}
	if !granted.Granted || granted.Lease == nil {
		return result, fmt.Errorf("controller changed while taking control; retry with its current state")
	}
	result.Granted, result.Lease = granted.Granted, granted.Lease
	resolve := ResolveOptions{Selector: m.ID, CWD: request.CWD, Env: request.Env}
	if plan.Abort && plan.Replace == "" {
		if _, err := a.agentControlWithLease(ctx, AgentControlOptions{ResolveOptions: resolve, CommandType: agent.CommandAbort, Actor: "operator", ControllerHolder: request.Holder, ControllerClientID: request.ClientID, ControllerLeaseID: granted.Lease.ID, ExpectedRuntimeID: status.RuntimeID}); err != nil {
			result.Warnings = append(result.Warnings, "could not abort the current turn: "+err.Error())
		}
	}
	if plan.Replace != "" {
		if err := a.stopRuntimeForReplacement(ctx, m, status.RuntimeID, status.Busy); err != nil {
			return result, err
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("stopped the %s runtime so it can resume in %s mode", status.RuntimeMode, plan.Replace))
	}
	mode := plan.Replace
	if mode == "" {
		mode = plan.Start
	}
	if mode != "" && request.EnsureRuntime {
		ensured, err := a.EnsureAgentRuntime(ctx, resolve, agent.Mode(mode))
		if err != nil {
			return result, fmt.Errorf("took control, but could not start the %s runtime: %w", mode, err)
		}
		result.Warnings = append(result.Warnings, ensured.Warnings...)
		runtime := ensured.Runtime
		if err := a.waitForAgentRuntimeReady(ctx, m.ID, &runtime); err != nil {
			return result, fmt.Errorf("took control, but the %s runtime is not ready: %w", mode, err)
		}
	}
	if runtime, err := a.Store.LoadAgentRuntime(m.ID); err == nil && runtime != nil && a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		result.Runtime = runtime
		if runtime.Mode == string(agent.ModeTUI) {
			if a.tmuxTerminalExists(ctx, m) {
				if err := a.syncPaneInput(ctx, m, request.Holder == model.LeaseHolderTerminal, plan); err != nil {
					return result, fmt.Errorf("control was taken, but terminal input could not be updated: %w", err)
				}
			}
		}
	}
	return result, nil
}

// syncPaneInput makes the agent pane writable only for a terminal controller
// and tells attached terminals when another client takes over.
func (a *App) syncPaneInput(ctx context.Context, m model.Manifest, enabled bool, plan ControlPlan) error {
	panes := a.paneControl()
	if panes == nil {
		return fmt.Errorf("terminal input control is unavailable")
	}
	target := agentPaneTarget(m)
	if err := panes.SetPaneInput(ctx, target, enabled); err != nil {
		return err
	}
	if !enabled && (plan.DisplacesTerminal || plan.Displaces != nil && plan.Displaces.Holder == model.LeaseHolderTerminal) {
		_ = panes.DisplayMessage(ctx, target, "wi: another client took control; this terminal is read-only. Run `wi switch --take` to take it back.")
	}
	return nil
}

// stopRuntimeForReplacement stops the runtime, aborting a turn in flight, and
// waits for the process to exit so a new owner can resume the conversation.
func (a *App) stopRuntimeForReplacement(ctx context.Context, m model.Manifest, expectedRuntimeID string, busy bool) error {
	_, operation, err := a.lockItemOperation(ctx, ResolveOptions{Selector: m.ID})
	if err != nil {
		return err
	}
	// Check and stop the planned owner under the resource operation lock.
	// Release it before waiting so process-host cleanup is not blocked.
	stop := func() (*model.AgentRuntime, error) {
		defer operation.Release()
		runtime, err := a.Store.LoadAgentRuntime(m.ID)
		if err != nil {
			return nil, err
		}
		if runtime != nil && runtime.ID != expectedRuntimeID {
			return nil, fmt.Errorf("runtime owner changed before takeover; retry")
		}
		if !a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
			return runtime, nil
		}
		if _, err := a.primaryAgentService().Stop(ctx, contract.ResolveOptions{Selector: m.ID}, busy); err != nil {
			return nil, fmt.Errorf("stop agent runtime %s for takeover: %w", runtime.ID, err)
		}
		return runtime, nil
	}
	runtime, err := stop()
	if err != nil {
		return err
	}
	timeout := a.ArchiveRuntimeStopTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		if !time.Now().Before(deadline) {
			return fmt.Errorf("agent runtime %s did not exit within %s; control was taken but the runtime was not replaced", runtime.ID, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

// ReleaseControl gives up a lease the client holds.
func (a *App) ReleaseControl(ctx context.Context, opts ResolveOptions, holder, clientID string) error {
	if a.Leases == nil {
		return nil
	}
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return err
	}
	unlock, err := a.lockController(ctx, m.ID)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := a.Leases.Lease(ctx, m.ID)
	if err != nil {
		return err
	}
	if current == nil || !current.SameClient(holder, clientID) {
		return nil
	}
	released, err := a.Leases.ReleaseLease(ctx, model.LeaseRequest{ItemID: m.ID, Holder: holder, ClientID: clientID, ExpectedLeaseID: &current.ID})
	if err != nil {
		return err
	}
	if released.Granted {
		_, _ = a.controlStatus(ctx, m, true)
	}
	return nil
}
