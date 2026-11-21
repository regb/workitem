// Package apiview translates API messages into presentation-only view types.
// It does not load storage, inspect processes, or execute application use cases.
package apiview

import (
	"strconv"
	"strings"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/itemnotes"
	"github.com/regb/workitem/internal/model"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Strings returns values, or an empty list instead of nil, so JSON clients
// that expect a list never receive null.
func Strings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func Time(value *timestamppb.Timestamp) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.AsTime()
}
func State(value v1.ItemState) string {
	return strings.ToLower(strings.TrimPrefix(value.String(), "ITEM_STATE_"))
}
func Resolution(value v1.Resolution) string {
	if value == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(value.String(), "RESOLUTION_"))
}
func Mode(value v1.AgentMode) string {
	if value == v1.AgentMode_AGENT_MODE_TUI {
		return "tui"
	}
	if value == v1.AgentMode_AGENT_MODE_HEADLESS {
		return "headless"
	}
	return ""
}
func Status(value v1.AgentStatus) string {
	if value == 0 {
		return "unknown"
	}
	return strings.ToLower(strings.TrimPrefix(value.String(), "AGENT_STATUS_"))
}
func Worktree(value v1.WorktreeStatus) string {
	if value == 0 {
		return "unknown"
	}
	return strings.ToLower(strings.TrimPrefix(value.String(), "WORKTREE_STATUS_"))
}
func Blockers(values []*v1.Blocker) []model.Blocker {
	out := []model.Blocker{}
	for _, v := range values {
		b := model.Blocker{ID: v.Id, Kind: strings.ToLower(strings.TrimPrefix(v.Kind.String(), "BLOCKER_KIND_")), ItemID: v.ItemId, Event: v.Event, Label: v.Label, Meta: v.Meta, Provider: v.Provider, FollowUp: v.FollowUp, CreatedAt: Time(v.CreatedAt)}
		if v.Until != nil {
			t := v.Until.AsTime()
			b.Until = &t
		}
		out = append(out, b)
	}
	return out
}
func Manifest(v *v1.Item) model.Manifest {
	if v == nil {
		return model.Manifest{}
	}
	m := model.Manifest{ID: v.Id, Slug: v.Slug, Title: v.Title, State: State(v.State), Resolution: Resolution(v.Resolution), Labels: Strings(v.Labels), DeepWork: v.DeepWork, Agent: v.AgentProfile, AgentSettings: v.AgentSettings, Blockers: Blockers(v.Blockers), CreatedAt: Time(v.CreatedAt), UpdatedAt: Time(v.UpdatedAt), StateChangedAt: Time(v.StateChangedAt), Priority: v.Priority, Pinned: v.Pinned}
	if v.Repository != nil {
		m.Repository = model.Repository{ID: v.Repository.Id, Name: v.Repository.Name, CreatedFromCommit: v.CreatedFromCommit}
	}
	if v.Checkout != nil {
		m.Checkout.Specification = model.CheckoutSpecification{Branch: v.Checkout.Branch, Kind: v.Checkout.Kind, Executor: v.Checkout.Executor}
		if c := v.Checkout.Claim; c != nil {
			m.Checkout.Claim = &model.WorkspaceClaim{ID: c.Id, Generation: uint64(c.Generation), Path: c.Path, Slot: c.Slot, RepositoryKey: m.Repository.ID, Executor: v.Checkout.Executor}
			m.Checkout.ClaimGeneration = uint64(c.Generation)
		}
	}
	if c := v.Conversation; c != nil {
		m.PrimaryConversation = &model.Conversation{ID: c.Id, Driver: c.Driver}
	}
	for _, f := range v.FollowUps {
		m.FollowUps = append(m.FollowUps, model.FollowUp{ID: f.Id, Message: f.Message, BlockedAt: Time(f.BlockedAt), ClearedAt: Time(f.ClearedAt)})
	}
	return m
}
func Runtime(itemID string, v *v1.Agent) app.AgentRuntimeStatusResult {
	out := app.AgentRuntimeStatusResult{WorkItemID: itemID, Warnings: []string{}}
	if v == nil {
		return out
	}
	out.Online = v.Online
	if c := v.Capabilities; c != nil {
		channel := ""
		switch c.Control {
		case v1.ControlChannel_CONTROL_CHANNEL_STRUCTURED:
			channel = model.ControlStructured
		case v1.ControlChannel_CONTROL_CHANNEL_EMULATED:
			channel = model.ControlEmulated
		}
		out.Capabilities = model.RuntimeCapabilities{Control: channel, IdleGuard: c.AtomicIdle, LiveContent: c.LiveContent, ImageInput: c.ImageInput, Prompt: c.Prompt, Steering: c.Steering, FollowUp: c.FollowUp, Cancel: c.Cancel, Settings: c.LiveSettings, Answer: c.Answer, NativeTUI: v.Mode == v1.AgentMode_AGENT_MODE_TUI, Headless: v.Mode == v1.AgentMode_AGENT_MODE_HEADLESS}
	}
	if v.Generation != 0 {
		out.Runtime = &model.AgentRuntime{ID: strconv.FormatUint(uint64(v.Generation), 10), Generation: v.Generation, WorkItemID: itemID, Mode: Mode(v.Mode), StartedAt: Time(v.StartedAt), Capabilities: out.Capabilities, Settings: v.RunningSettings}
	}
	return out
}
func Lease(v *v1.ControlLease) *model.ControlLease {
	if v == nil {
		return nil
	}
	out := &model.ControlLease{ID: v.Id, ItemID: v.ItemId, Holder: v.Holder, ClientID: v.ClientId, Label: v.Label, AcquiredAt: Time(v.AcquiredAt)}
	if v.ExpiresAt != nil {
		t := v.ExpiresAt.AsTime()
		out.ExpiresAt = &t
	}
	return out
}
func Control(itemID string, v *v1.ControlStatus, a *v1.Agent) (app.ControlStatus, app.ControlPlan) {
	status := app.ControlStatus{WorkItemID: itemID}
	plan := app.ControlPlan{}
	if v == nil {
		return status, plan
	}
	status.Lease = Lease(v.Lease)
	status.Implicit = v.Implicit
	if v.Lease != nil {
		status.Holder = v.Lease.Holder
	} else if v.Implicit {
		// An attached terminal controls the agent without holding a lease.
		status.Holder = model.LeaseHolderTerminal
	}
	if p := v.Plan; p != nil {
		plan = app.ControlPlan{PlanToken: p.PlanToken, NeedsConfirmation: p.NeedsConfirmation, Reasons: Strings(p.Reasons), Displaces: Lease(p.Displaces), DisplacesTerminal: p.DisplacesTerminal, Abort: p.AbortsTurn, Replace: Mode(p.RestartsIn), Start: Mode(p.StartsIn), Unavailable: p.Unavailable}
	}
	plan.Reasons = Strings(plan.Reasons)
	if a != nil {
		status.RuntimeOnline = a.Online
		status.RuntimeMode = Mode(a.Mode)
		status.RuntimeID = strconv.FormatUint(uint64(a.Generation), 10)
		status.Busy = a.Status == v1.AgentStatus_AGENT_STATUS_BUSY
		if a.Terminal != nil {
			status.TerminalClients = int(a.Terminal.AttachedClients)
		}
		r := Runtime(itemID, a)
		status.Channels = r.Capabilities.Channels()
	}
	return status, plan
}
func Attachment(v *v1.Attachment) itemnotes.Attachment {
	if v == nil {
		return itemnotes.Attachment{}
	}
	return itemnotes.Attachment{Name: v.Name, SHA256: v.Sha256, Size: int64(v.Size), Type: v.ContentType, Path: v.Path}
}
