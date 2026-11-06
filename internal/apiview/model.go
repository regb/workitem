// Package apiview translates API messages into presentation-only view types.
// It does not load storage, inspect processes, or execute application use cases.
package apiview

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	driver "github.com/regb/workitem/agentdriver/protocol"
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
		b := model.Blocker{ID: v.Id, Kind: strings.ToLower(strings.TrimPrefix(v.Kind.String(), "BLOCKER_KIND_")), ItemID: v.ItemId, Event: v.Event, Label: v.Label, Meta: v.Meta, CreatedAt: Time(v.CreatedAt)}
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
		out.Capabilities = model.RuntimeCapabilities{Control: channel, IdleGuard: c.AtomicIdle, LiveContent: c.LiveContent, ImageInput: c.ImageInput, Prompt: c.Prompt, Steering: c.Steering, FollowUp: c.FollowUp, Cancel: c.Cancel, Settings: c.LiveSettings, NativeTUI: v.Mode == v1.AgentMode_AGENT_MODE_TUI, Headless: v.Mode == v1.AgentMode_AGENT_MODE_HEADLESS}
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
func Note(v *v1.Note) itemnotes.Note {
	out := itemnotes.Note{Time: Time(v.Time), Text: v.Text, Head: v.Head}
	if v.Attachment != nil {
		a := Attachment(v.Attachment)
		out.Attachment = &a
	}
	return out
}
func images(values []*v1.Image) []driver.HistoryImage {
	out := []driver.HistoryImage{}
	for _, v := range values {
		out = append(out, driver.HistoryImage{MIMEType: v.MimeType, Data: base64.StdEncoding.EncodeToString(v.Data), Alt: v.Alt})
	}
	return out
}
func subagent(v *v1.SubagentRef) *driver.HistorySubagent {
	if v == nil {
		return nil
	}
	return &driver.HistorySubagent{ID: v.Id, Type: v.Type, Description: v.Description, Status: v.Status}
}
func tool(v *v1.ToolCall) driver.HistoryTool {
	return driver.HistoryTool{ID: v.Id, Name: v.Name, Input: v.Input, Output: v.Output, Status: strings.ToLower(strings.TrimPrefix(v.Status.String(), "TOOL_CALL_STATUS_")), Images: images(v.Images), Subagent: subagent(v.Subagent)}
}
func History(v *v1.GetHistoryResponse) driver.History {
	out := driver.History{Messages: []driver.HistoryMessage{}}
	if v == nil {
		return out
	}
	out.LeafID = v.LeafId
	out.BeforeID = v.NextBefore
	out.HasMore = v.NextBefore != ""
	out.Truncated = v.Truncated
	out.Warnings = v.Warnings
	for _, m := range v.Messages {
		message := driver.HistoryMessage{ID: m.Id, NativeIDs: m.NativeIds, Role: strings.ToLower(strings.TrimPrefix(m.Role.String(), "MESSAGE_ROLE_")), Text: m.Text, Images: images(m.Images), Error: m.Error, Aborted: m.Aborted, Subagent: subagent(m.Subagent)}
		if m.Time != nil {
			message.Timestamp = m.Time.AsTime().Format(time.RFC3339Nano)
		}
		for _, t := range m.Tools {
			message.Tools = append(message.Tools, tool(t))
		}
		if m.Reasoning != nil {
			message.Reasoning = &driver.HistoryReasoning{Text: m.Reasoning.Text, DurationMS: int64(m.Reasoning.DurationMs)}
		}
		if m.Summary != nil {
			message.Summary = &driver.HistorySummary{Kind: m.Summary.Kind, TokensBefore: int(m.Summary.TokensBefore)}
		}
		out.Messages = append(out.Messages, message)
	}
	return out
}
func Content(v *v1.LiveContent) driver.Content {
	out := driver.Content{ProtocolVersion: driver.ContentVersion}
	if v == nil {
		return out
	}
	out.RuntimeID = strconv.FormatUint(uint64(v.Generation), 10)
	out.MessageID = v.MessageId
	out.Text = v.Text
	out.Thought = v.Thought
	out.Activity = v.Activity
	out.SavedAs = v.SavedAs
	out.Truncated = v.Truncated
	out.Error = v.Error
	for _, t := range v.Tools {
		out.Tools = append(out.Tools, driver.ContentTool{ID: t.Id, Name: t.Name, Status: strings.ToLower(strings.TrimPrefix(t.Status.String(), "TOOL_CALL_STATUS_")), Input: t.Input, Output: t.Output})
	}
	for _, p := range v.Prompts {
		out.Prompts = append(out.Prompts, driver.PromptLink{RequestID: p.ClientMessageId, NativeIDs: p.NativeIds})
	}
	return out
}
func ContentEvent(v *v1.WatchLiveContentResponse, sequence uint64) driver.ContentEvent {
	out := driver.ContentEvent{Sequence: sequence}
	switch e := v.Event.(type) {
	case *v1.WatchLiveContentResponse_Snapshot:
		out.Type = driver.ContentSnapshot
		value := Content(e.Snapshot)
		value.Sequence = sequence
		out.Snapshot = &value
	case *v1.WatchLiveContentResponse_MessageStarted:
		out.Type = driver.ContentMessageStarted
		out.MessageID = e.MessageStarted.MessageId
	case *v1.WatchLiveContentResponse_MessageDelta:
		out.Type = driver.ContentMessageDelta
		out.MessageID = e.MessageDelta.MessageId
		out.Text = e.MessageDelta.Text
		out.Truncated = e.MessageDelta.Truncated
	case *v1.WatchLiveContentResponse_MessageReplaced:
		out.Type = driver.ContentMessageReplaced
		out.MessageID = e.MessageReplaced.MessageId
		out.Text = e.MessageReplaced.Text
		out.Truncated = e.MessageReplaced.Truncated
	case *v1.WatchLiveContentResponse_MessageCompleted:
		out.Type = driver.ContentMessageCompleted
		out.MessageID = e.MessageCompleted.MessageId
	case *v1.WatchLiveContentResponse_MessageSaved:
		out.Type = driver.ContentMessageSaved
		out.MessageID = e.MessageSaved.MessageId
		out.NativeIDs = e.MessageSaved.NativeIds
	case *v1.WatchLiveContentResponse_ThoughtDelta:
		out.Type = driver.ContentThoughtDelta
		out.MessageID = e.ThoughtDelta.MessageId
		out.Text = e.ThoughtDelta.Text
	case *v1.WatchLiveContentResponse_Activity:
		out.Type = driver.ContentActivity
		out.Activity = e.Activity.Activity
	case *v1.WatchLiveContentResponse_PromptSaved:
		out.Type = driver.ContentPromptSaved
		if e.PromptSaved.Link != nil {
			out.RequestID = e.PromptSaved.Link.ClientMessageId
			out.NativeIDs = e.PromptSaved.Link.NativeIds
		}
	case *v1.WatchLiveContentResponse_TurnSettled:
		out.Type = driver.ContentTurnSettled
	case *v1.WatchLiveContentResponse_Tool:
		out.Type = driver.ContentToolUpdated
		switch e.Tool.Phase {
		case v1.ToolCallPhase_TOOL_CALL_PHASE_STARTED:
			out.Type = driver.ContentToolStarted
		case v1.ToolCallPhase_TOOL_CALL_PHASE_COMPLETED:
			out.Type = driver.ContentToolCompleted
		case v1.ToolCallPhase_TOOL_CALL_PHASE_CLEARED:
			out.Type = driver.ContentToolCleared
		}
		if t := e.Tool.Tool; t != nil {
			value := tool(t)
			out.Tool = &driver.ContentTool{ID: value.ID, Name: value.Name, Input: value.Input, Output: value.Output, Status: value.Status}
		}
	}
	return out
}

// stringMap returns values, or an empty map instead of nil, so JSON clients
// that read it as an object never receive null.
func stringMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	return values
}

func Settings(v *v1.GetAgentSettingsResponse) app.AgentSettingsView {
	out := app.AgentSettingsView{Agent: v.AgentProfile, Driver: v.Driver, Values: stringMap(v.Values), Overrides: stringMap(v.Overrides), Running: v.Running, CatalogError: v.CatalogError}
	if c := v.Catalog; c != nil {
		out.Catalog = &driver.ModelCatalog{Default: c.DefaultModel, DefaultReasoning: c.DefaultReasoning, ReasoningLevels: c.ReasoningLevels}
		for _, m := range c.Models {
			out.Catalog.Models = append(out.Catalog.Models, driver.ModelOption{Value: m.Value, Resolved: m.Resolved, Label: m.Label, Description: m.Description, Reasoning: m.Reasoning, Levels: m.Levels, ImageInput: m.ImageInput})
		}
	}
	return out
}
