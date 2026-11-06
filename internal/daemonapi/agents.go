package daemonapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"encoding/base64"
	"errors"
	"fmt"
	"github.com/regb/workitem/internal/coordinator"
	"maps"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	driver "github.com/regb/workitem/agentdriver/protocol"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"google.golang.org/protobuf/proto"
)

func (s *Server) agent(ctx context.Context, itemID string) (*v1.Agent, *v1.ControlStatus, []string, error) {
	m, err := s.App.Store.LoadManifest(itemID)
	if err != nil {
		return nil, nil, nil, err
	}
	runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(itemID))
	if err != nil {
		return nil, nil, nil, err
	}
	out := &v1.Agent{AgentProfile: m.Agent, Online: runtime.Online}
	if profile, ok := s.App.Agents()[m.Agent]; ok {
		out.Driver = profile.Driver
	}
	observation := s.observation(ctx, m)
	out.Status = observation.AgentStatus
	out.Reason = observation.AgentReason
	out.Asking = observation.Asking
	if r := runtime.Runtime; r != nil {
		out.Generation = r.Generation
		if out.Generation == 0 {
			out.Generation = 1
		}
		out.Mode = modeValue(r.Mode)
		out.StartedAt = stamp(r.StartedAt)
		out.RunningSettings = r.Settings
		c := r.Capabilities
		channel := v1.ControlChannel_CONTROL_CHANNEL_NONE
		switch c.ControlChannel() {
		case model.ControlStructured:
			channel = v1.ControlChannel_CONTROL_CHANNEL_STRUCTURED
		case model.ControlEmulated:
			channel = v1.ControlChannel_CONTROL_CHANNEL_EMULATED
		}
		out.Capabilities = &v1.AgentCapabilities{Control: channel, Prompt: c.Prompt, Steering: c.Steering, FollowUp: c.FollowUp, Cancel: c.Cancel, AtomicIdle: c.IdleGuard, LiveContent: c.LiveContent, ImageInput: c.ImageInput, LiveSettings: c.Settings}
		if runtime.Online && r.Mode == "tui" {
			tmp := os.Getenv("TMUX_TMPDIR")
			if tmp == "" {
				tmp = "/tmp"
			}
			out.Terminal = &v1.Terminal{TmuxSocket: filepath.Join(tmp, fmt.Sprintf("tmux-%d", os.Geteuid()), "default"), Session: m.TerminalSessionName(), Window: "agent"}
		}
	}
	status, plan, err := s.App.PlanControl(ctx, s.controlRequest(ctx, itemID, caller(ctx).client, false, false))
	s.rememberLease(status.Lease)
	control := s.controlStatus(ctx, status, plan, caller(ctx).client)
	if err != nil {
		runtime.Warnings = append(runtime.Warnings, err.Error())
	}
	if out.Terminal != nil {
		out.Terminal.AttachedClients = uint32(status.TerminalClients)
	}
	return out, control, runtime.Warnings, nil
}
func (s *Server) GetAgent(ctx context.Context, req *connect.Request[v1.GetAgentRequest]) (*connect.Response[v1.GetAgentResponse], error) {
	value, control, warnings, err := s.agent(ctx, req.Msg.ItemId)
	return response(&v1.GetAgentResponse{Agent: value, Control: control, Warnings: warnings}, err)
}
func (s *Server) startAgent(ctx context.Context, itemID string, wanted v1.AgentMode) error {
	m, err := s.requireActive(ctx, itemID)
	if err != nil {
		return err
	}
	if m.Checkout.Claim == nil {
		return precondition("agent requires a workspace claim")
	}
	setup := s.setup(ctx, itemID)
	if setupBlocksAgent(setup) {
		return precondition("workspace setup is running or failed; wait for it or retry it")
	}
	running, err := s.App.AgentRuntimeStatus(ctx, resolve(itemID))
	if err != nil {
		return err
	}
	if running.Online && running.Runtime != nil && running.Runtime.Mode != mode(wanted) {
		if s.App.AgentBusy(ctx, itemID) {
			return precondition("agent is busy in another mode")
		}
		if _, err := s.App.StopAgentRuntime(ctx, resolve(itemID), false); err != nil {
			return err
		}
		if err := s.App.WaitForAgentStopped(ctx, itemID); err != nil {
			return partial(err, itemID, "requested agent shutdown")
		}
	}
	result, err := s.App.EnsureAgentRuntime(ctx, resolve(itemID), agent.Mode(mode(wanted)))
	if err != nil {
		return err
	}
	return s.App.WaitForAgentStarted(ctx, itemID, &result.Runtime)
}
func (s *Server) StartAgent(ctx context.Context, req *connect.Request[v1.StartAgentRequest]) (*connect.Response[v1.StartAgentResponse], error) {
	m, err := s.requireActive(ctx, req.Msg.ItemId)
	if err != nil {
		return nil, rpcError(err)
	}
	if m.Checkout.Claim == nil {
		return nil, precondition("agent requires a workspace claim")
	}
	if req.Msg.Mode < 0 || req.Msg.Mode > 2 {
		return nil, invalid("invalid agent mode")
	}
	setup := s.setup(ctx, m.ID)
	if setupBlocksAgent(setup) {
		return nil, precondition("workspace setup is running or failed; wait for it or retry it")
	}
	if err := s.App.ValidateItemAgentConfig(ctx, resolve(m.ID)); err != nil {
		return nil, rpcError(err)
	}
	current, _, _, err := s.agent(ctx, m.ID)
	if err != nil {
		return nil, rpcError(err)
	}
	if current.Online && current.Mode == modeValue(mode(req.Msg.Mode)) {
		return response(&v1.StartAgentResponse{Agent: current}, nil)
	}
	if current.Online && s.App.AgentBusy(ctx, m.ID) {
		return nil, precondition("agent is busy")
	}
	job := s.startJobKey("start_agent", "start_agent:"+mode(req.Msg.Mode), m.ID, "", func(ctx context.Context, j *jobRun) error {
		unlock := s.lock(m.ID)
		defer unlock()
		j.phase("starting agent")
		return s.startAgent(ctx, m.ID, req.Msg.Mode)
	})
	return response(&v1.StartAgentResponse{Agent: current, Job: job}, nil)
}
func (s *Server) confirmTerminal(ctx context.Context, itemID string, confirm bool) error {
	status, err := s.App.ControlStatus(ctx, resolve(itemID))
	if err != nil {
		return err
	}
	if status.TerminalClients > 0 && !confirm {
		return precondition("attached terminal clients require confirm_attached_terminal")
	}
	return nil
}
func (s *Server) StopAgent(ctx context.Context, req *connect.Request[v1.StopAgentRequest]) (*connect.Response[v1.StopAgentResponse], error) {
	if err := s.confirmTerminal(ctx, req.Msg.ItemId, req.Msg.ConfirmAttachedTerminal); err != nil {
		return nil, rpcError(err)
	}
	if !req.Msg.Force && s.App.AgentBusy(ctx, req.Msg.ItemId) {
		return nil, precondition("agent is busy")
	}
	result, err := s.App.StopAgentRuntime(ctx, resolve(req.Msg.ItemId), req.Msg.Force)
	if err == nil {
		if waitErr := s.App.WaitForAgentStopped(ctx, req.Msg.ItemId); waitErr != nil {
			return nil, partial(waitErr, req.Msg.ItemId, "requested agent shutdown")
		}
	}
	return response(&v1.StopAgentResponse{Warnings: result.Warnings}, err)
}
func (s *Server) generation(ctx context.Context, itemID string, expected uint32) (*v1.Agent, error) {
	value, _, _, err := s.agent(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if expected != 0 && expected != value.Generation {
		return nil, aborted("agent generation changed")
	}
	return value, nil
}
func (s *Server) RestartAgent(ctx context.Context, req *connect.Request[v1.RestartAgentRequest]) (*connect.Response[v1.RestartAgentResponse], error) {
	current, err := s.generation(ctx, req.Msg.ItemId, req.Msg.ExpectedGeneration)
	if err != nil {
		return nil, rpcError(err)
	}
	if !current.Online {
		return nil, precondition("agent is not running")
	}
	if s.App.AgentBusy(ctx, req.Msg.ItemId) {
		return nil, precondition("agent is busy")
	}
	job := s.startJobKey("restart_agent", "restart_agent:"+fmt.Sprint(current.Generation), req.Msg.ItemId, "", func(ctx context.Context, j *jobRun) error {
		unlock := s.lock(req.Msg.ItemId)
		defer unlock()
		if _, err := s.generation(ctx, req.Msg.ItemId, current.Generation); err != nil {
			return err
		}
		j.phase("restarting agent")
		_, err := s.App.RestartAgent(ctx, resolve(req.Msg.ItemId))
		if err != nil {
			after, _, _, readErr := s.agent(ctx, req.Msg.ItemId)
			if readErr == nil && (!after.Online || after.Generation != current.Generation) {
				return partial(err, req.Msg.ItemId, "stopped previous agent")
			}
		}
		return err
	})
	return response(&v1.RestartAgentResponse{Agent: current, Job: job}, nil)
}
func (s *Server) controlLease(ctx context.Context, itemID, leaseID string, takeVacant bool) (*model.ControlLease, error) {
	lease, err := s.Local.Lease(ctx, itemID)
	if err != nil {
		return nil, err
	}
	who := caller(ctx)
	if lease != nil {
		if lease.ClientID != who.client || leaseID != "" && lease.ID != leaseID {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("caller does not hold the control lease"))
		}
		return lease, nil
	}
	if !takeVacant {
		return nil, precondition("caller has no control lease")
	}
	// Taking vacant control may replace the agent with one this caller can
	// drive (a TUI with a headless one); the replacement must be started.
	result, err := s.App.AcquireControl(ctx, s.controlRequest(ctx, itemID, who.client, false, true))
	if err != nil {
		return nil, err
	}
	if result.Lease == nil {
		return nil, precondition("control lease was not granted")
	}
	s.rememberLease(result.Lease)
	return result.Lease, nil
}
func (s *Server) send(ctx context.Context, r *v1.SendMessageRequest) error {
	var receipt coordinator.APIMessageReceipt
	pending := false
	if r.ClientMessageId != "" {
		var err error
		receipt, pending, err = s.Core.Database().APIMessage(r.ItemId, r.ClientMessageId)
		if err != nil {
			return err
		}
		if pending && receipt.Delivered {
			return nil
		}
	}
	current, err := s.generation(ctx, r.ItemId, r.ExpectedGeneration)
	if err != nil {
		return err
	}
	if !current.Online {
		return precondition("agent is not running")
	}
	if pending && receipt.Generation != current.Generation {
		return aborted("message delivery was reserved on an older agent generation; inspect history before retrying")
	}
	busy := s.App.AgentBusy(ctx, r.ItemId)
	delivery := "prompt"
	switch r.Delivery {
	case v1.Delivery_DELIVERY_UNSPECIFIED:
		if busy {
			delivery = "steer"
		}
	case v1.Delivery_DELIVERY_PROMPT:
		if busy && !pending {
			return precondition("agent is busy; use steer or follow_up")
		}
	case v1.Delivery_DELIVERY_STEER:
		delivery = "steer"
		if !busy && !pending {
			return precondition("steer requires a busy agent")
		}
	case v1.Delivery_DELIVERY_FOLLOW_UP:
		delivery = "follow_up"
	default:
		return invalid("unknown message delivery")
	}
	lease, err := s.controlLease(ctx, r.ItemId, r.LeaseId, true)
	if err != nil {
		return err
	}
	images := []agent.ImageContent{}
	for _, image := range r.Images {
		images = append(images, agent.ImageContent{MIMEType: image.MimeType, Data: base64.StdEncoding.EncodeToString(image.Data)})
	}
	runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(r.ItemId))
	if err != nil {
		return err
	}
	if runtime.Runtime == nil {
		return precondition("agent stopped before delivery")
	}
	commandID := ""
	if r.ClientMessageId != "" {
		sum := sha256.Sum256([]byte(r.ItemId + "\x00" + r.ClientMessageId))
		commandID = "api-message:" + hex.EncodeToString(sum[:])
		if !pending {
			receipt = coordinator.APIMessageReceipt{ItemID: r.ItemId, MessageID: r.ClientMessageId, RuntimeID: runtime.Runtime.ID, Generation: current.Generation, CreatedAt: time.Now().UTC()}
		}
		if receipt.RuntimeID != runtime.Runtime.ID {
			return aborted("message delivery belongs to another runtime")
		}
		if err := s.Core.Database().RecordAPIMessage(receipt); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
	}
	_, err = s.App.AgentControl(ctx, app.AgentControlOptions{ResolveOptions: resolve(r.ItemId), CommandType: delivery, Message: r.Text, Images: images, Actor: controlActor(ctx), RequestID: r.ClientMessageId, CommandID: commandID, ExpectedRuntimeID: runtime.Runtime.ID, RequireIdle: delivery == "prompt" && runtime.Runtime.Capabilities.IdleGuard, ControllerHolder: lease.Holder, ControllerClientID: lease.ClientID, ControllerLeaseID: lease.ID})
	if r.ClientMessageId != "" {
		if err != nil {
			var refused *driver.ControlRejectedError
			if errors.Is(err, driver.ErrNotIdle) || errors.As(err, &refused) {
				_ = s.Core.Database().ForgetAPIMessage(r.ItemId, r.ClientMessageId)
			}
			return err
		}
		receipt.Delivered = true
		if err := s.Core.Database().RecordAPIMessage(receipt); err != nil {
			return partial(err, r.ItemId, "submitted message")
		}
	}
	return err
}
func (s *Server) SendMessage(ctx context.Context, req *connect.Request[v1.SendMessageRequest]) (*connect.Response[v1.SendMessageResponse], error) {
	r := req.Msg
	if len(r.ClientMessageId) > 200 || strings.TrimSpace(r.ClientMessageId) != r.ClientMessageId {
		return nil, invalid("client_message_id must be at most 200 bytes without surrounding whitespace")
	}
	if r.ClientMessageId != "" {
		receipt, found, err := s.Core.Database().APIMessage(r.ItemId, r.ClientMessageId)
		if err != nil {
			return nil, rpcError(err)
		}
		if found && receipt.Delivered {
			return response(&v1.SendMessageResponse{Generation: receipt.Generation}, nil)
		}
	}
	m, err := s.requireActive(ctx, r.ItemId)
	if err != nil {
		return nil, rpcError(err)
	}
	if m.Checkout.Claim == nil {
		return nil, precondition("message requires a workspace claim")
	}
	if err := validateMessage(r); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.Text) == "" && len(r.Images) == 0 {
		return nil, invalid("message text or images are required")
	}
	current, err := s.generation(ctx, r.ItemId, r.ExpectedGeneration)
	if err != nil {
		return nil, rpcError(err)
	}
	if r.RequireOnline && !current.Online {
		return nil, precondition("agent is not running")
	}
	if current.Online {
		err = s.send(ctx, r)
		return response(&v1.SendMessageResponse{Generation: current.Generation}, err)
	}
	if r.Delivery == v1.Delivery_DELIVERY_STEER || r.Delivery == v1.Delivery_DELIVERY_FOLLOW_UP {
		return nil, precondition("delivery requires a running, busy agent")
	}
	if r.ClientMessageId != "" {
		_, pending, err := s.Core.Database().APIMessage(r.ItemId, r.ClientMessageId)
		if err != nil {
			return nil, rpcError(err)
		}
		if pending {
			return nil, aborted("previous message delivery is uncertain on a stopped agent; inspect its history")
		}
	}
	if _, err := s.controlLease(ctx, r.ItemId, r.LeaseId, true); err != nil {
		return nil, rpcError(err)
	}
	who := caller(ctx)
	job := s.startJobKey("send_message", "send_message:"+r.ClientMessageId, r.ItemId, "", func(ctx context.Context, j *jobRun) error {
		ctx = context.WithValue(ctx, identityKey{}, who)
		unlock := s.lock(r.ItemId)
		defer unlock()
		if _, err := s.controlLease(ctx, r.ItemId, r.LeaseId, true); err != nil {
			return err
		}
		j.phase("starting agent")
		if err := s.startAgent(ctx, r.ItemId, r.StartMode); err != nil {
			return err
		}
		j.phase("delivering message")
		copy := proto.Clone(r).(*v1.SendMessageRequest)
		copy.ExpectedGeneration = 0
		return s.send(ctx, copy)
	})
	return response(&v1.SendMessageResponse{Generation: current.Generation, Job: job}, nil)
}
func (s *Server) AbortTurn(ctx context.Context, req *connect.Request[v1.AbortTurnRequest]) (*connect.Response[v1.AbortTurnResponse], error) {
	lease, err := s.controlLease(ctx, req.Msg.ItemId, req.Msg.LeaseId, false)
	if err != nil {
		return nil, rpcError(err)
	}
	if !s.App.AgentBusy(ctx, req.Msg.ItemId) {
		return response(&v1.AbortTurnResponse{}, nil)
	}
	runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(req.Msg.ItemId))
	if err != nil {
		return nil, rpcError(err)
	}
	if runtime.Runtime == nil {
		return response(&v1.AbortTurnResponse{}, nil)
	}
	_, err = s.App.AgentControl(ctx, app.AgentControlOptions{ResolveOptions: resolve(req.Msg.ItemId), CommandType: agent.CommandAbort, Actor: controlActor(ctx), ExpectedRuntimeID: runtime.Runtime.ID, ControllerHolder: lease.Holder, ControllerClientID: lease.ClientID, ControllerLeaseID: lease.ID})
	return response(&v1.AbortTurnResponse{}, err)
}
func (s *Server) GetAgentSettings(ctx context.Context, req *connect.Request[v1.GetAgentSettingsRequest]) (*connect.Response[v1.GetAgentSettingsResponse], error) {
	value, err := s.App.AgentSettings(ctx, resolve(req.Msg.ItemId))
	out := &v1.GetAgentSettingsResponse{AgentProfile: value.Agent, Driver: value.Driver, Values: value.Values, Overrides: value.Overrides, Running: value.Running, CatalogError: value.CatalogError}
	if c := value.Catalog; c != nil {
		out.Catalog = &v1.ModelCatalog{ReasoningLevels: c.ReasoningLevels, DefaultModel: c.Default, DefaultReasoning: c.DefaultReasoning}
		for _, m := range c.Models {
			out.Catalog.Models = append(out.Catalog.Models, &v1.ModelOption{Value: m.Value, Resolved: m.Resolved, Label: m.Label, Description: m.Description, Reasoning: m.Reasoning, Levels: m.Levels, ImageInput: m.ImageInput})
		}
	}
	return response(out, err)
}
func (s *Server) UpdateAgentSettings(ctx context.Context, req *connect.Request[v1.UpdateAgentSettingsRequest]) (*connect.Response[v1.UpdateAgentSettingsResponse], error) {
	opts := app.SetAgentSettingsOptions{ResolveOptions: resolve(req.Msg.ItemId), Settings: req.Msg.Settings, Restart: req.Msg.Restart}
	runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(req.Msg.ItemId))
	if err != nil {
		return nil, rpcError(err)
	}
	if runtime.Online {
		lease, err := s.controlLease(ctx, req.Msg.ItemId, req.Msg.LeaseId, false)
		if err != nil {
			return nil, rpcError(err)
		}
		opts.ControllerHolder, opts.ControllerClientID, opts.ControllerLeaseID = lease.Holder, lease.ClientID, lease.ID
	}
	before, err := s.canonical(ctx, req.Msg.ItemId, 0)
	if err != nil {
		return nil, rpcError(err)
	}
	result, err := s.App.SetAgentSettings(ctx, opts)
	if err != nil {
		if after, readErr := s.canonical(ctx, req.Msg.ItemId, 0); readErr == nil && !maps.Equal(after.Manifest.AgentSettings, before.Manifest.AgentSettings) {
			return nil, partial(err, req.Msg.ItemId, "saved agent setting overrides")
		}
	}
	return response(&v1.UpdateAgentSettingsResponse{Overrides: result.Overrides, Changed: result.Changed, Applied: result.Applied, Pending: result.Pending, Restarted: result.Restarted, Warnings: result.Warnings}, err)
}
func (s *Server) SuggestTitle(ctx context.Context, req *connect.Request[v1.SuggestTitleRequest]) (*connect.Response[v1.SuggestTitleResponse], error) {
	description := req.Msg.Description
	if description == "" {
		shown, err := s.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: req.Msg.ItemId, IncludeDescription: true}))
		if err != nil {
			return nil, err
		}
		description = shown.Msg.Description
	}
	title, err := s.App.GenerateItemTitle(ctx, resolve(req.Msg.ItemId), description)
	return response(&v1.SuggestTitleResponse{Title: title}, err)
}
func subagent(s *driver.HistorySubagent) *v1.SubagentRef {
	if s == nil {
		return nil
	}
	return &v1.SubagentRef{Id: s.ID, Type: s.Type, Description: s.Description, Status: s.Status}
}
func historyImages(images []driver.HistoryImage) []*v1.Image {
	out := []*v1.Image{}
	for _, image := range images {
		data, _ := base64.StdEncoding.DecodeString(image.Data)
		out = append(out, &v1.Image{MimeType: image.MIMEType, Data: data, Alt: image.Alt})
	}
	return out
}
func tool(t driver.HistoryTool) *v1.ToolCall {
	return &v1.ToolCall{Id: t.ID, Name: t.Name, Input: t.Input, Output: t.Output, Status: v1.ToolCallStatus(v1.ToolCallStatus_value["TOOL_CALL_STATUS_"+strings.ToUpper(t.Status)]), Images: historyImages(t.Images), Subagent: subagent(t.Subagent)}
}
func history(value driver.History) *v1.GetHistoryResponse {
	out := &v1.GetHistoryResponse{LeafId: value.LeafID, NextBefore: value.BeforeID, Truncated: value.Truncated, Warnings: value.Warnings, Messages: []*v1.ConversationMessage{}}
	for _, m := range value.Messages {
		timestamp, _ := time.Parse(time.RFC3339Nano, m.Timestamp)
		row := &v1.ConversationMessage{Id: m.ID, NativeIds: m.NativeIDs, Role: v1.MessageRole(v1.MessageRole_value["MESSAGE_ROLE_"+strings.ToUpper(m.Role)]), Text: m.Text, Time: stamp(timestamp), Images: historyImages(m.Images), Error: m.Error, Aborted: m.Aborted, Subagent: subagent(m.Subagent)}
		for _, t := range m.Tools {
			row.Tools = append(row.Tools, tool(t))
		}
		if m.Reasoning != nil {
			row.Reasoning = &v1.Reasoning{Text: m.Reasoning.Text, DurationMs: uint32(max(0, m.Reasoning.DurationMS))}
		}
		if m.Summary != nil {
			row.Summary = &v1.Summary{Kind: m.Summary.Kind, TokensBefore: uint32(max(0, m.Summary.TokensBefore))}
		}
		out.Messages = append(out.Messages, row)
	}
	return out
}
func (s *Server) GetHistory(ctx context.Context, req *connect.Request[v1.GetHistoryRequest]) (*connect.Response[v1.GetHistoryResponse], error) {
	value, err := s.App.ConversationHistoryPage(ctx, resolve(req.Msg.ItemId), req.Msg.LeafId, req.Msg.Before)
	return response(history(value), err)
}
func (s *Server) GetSubagentHistory(ctx context.Context, req *connect.Request[v1.GetSubagentHistoryRequest]) (*connect.Response[v1.GetSubagentHistoryResponse], error) {
	value, err := s.App.SubagentHistoryPage(ctx, resolve(req.Msg.ItemId), req.Msg.SubagentId, req.Msg.Before)
	h := history(value)
	return response(&v1.GetSubagentHistoryResponse{Messages: h.Messages, NextBefore: h.NextBefore, Truncated: h.Truncated}, err)
}
func live(value driver.Content, generation uint32) *v1.LiveContent {
	out := &v1.LiveContent{Generation: generation, MessageId: value.MessageID, Text: value.Text, Thought: value.Thought, Activity: value.Activity, SavedAs: value.SavedAs, Error: value.Error, Truncated: value.Truncated}
	for _, t := range value.Tools {
		out.Tools = append(out.Tools, tool(driver.HistoryTool{ID: t.ID, Name: t.Name, Status: t.Status, Input: t.Input, Output: t.Output}))
	}
	for _, p := range value.Prompts {
		out.Prompts = append(out.Prompts, &v1.PromptLink{ClientMessageId: p.RequestID, NativeIds: p.NativeIDs})
	}
	return out
}
func (s *Server) WatchLiveContent(ctx context.Context, req *connect.Request[v1.WatchLiveContentRequest], stream *connect.ServerStream[v1.WatchLiveContentResponse]) error {
	if _, err := s.canonical(ctx, req.Msg.ItemId, 0); err != nil {
		return rpcError(err)
	}
	var previousGeneration uint32
	for {
		runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(req.Msg.ItemId))
		if err != nil {
			return rpcError(err)
		}
		generation := uint32(0)
		runtimeID := ""
		if runtime.Runtime != nil {
			generation = runtime.Runtime.Generation
			runtimeID = runtime.Runtime.ID
		}
		if !runtime.Online {
			if err := stream.Send(&v1.WatchLiveContentResponse{Cursor: fmt.Sprintf("%s:0", runtimeID), Event: &v1.WatchLiveContentResponse_Snapshot{Snapshot: &v1.LiveContent{Generation: generation}}}); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		previousGeneration = generation
		err = s.App.AgentContentStream(ctx, resolve(req.Msg.ItemId), func(event driver.ContentEvent) error {
			// Native streams always begin with a snapshot. Sending a normalized snapshot
			// per change also safely resets cursors that cannot be replayed by the driver.
			return s.sendContentEvent(stream, runtimeID, generation, event)
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		current, e := s.App.AgentRuntimeStatus(ctx, resolve(req.Msg.ItemId))
		if e != nil {
			return rpcError(e)
		}
		if current.Runtime != nil && current.Runtime.Generation == previousGeneration && current.Online {
			return rpcError(err)
		}
	}
}
func (s *Server) sendContentEvent(stream *connect.ServerStream[v1.WatchLiveContentResponse], runtimeID string, generation uint32, e driver.ContentEvent) error {
	out := &v1.WatchLiveContentResponse{Cursor: base64.RawURLEncoding.EncodeToString([]byte(runtimeID + ":" + strconv.FormatUint(e.Sequence, 10)))}
	switch e.Type {
	case driver.ContentSnapshot:
		if e.Snapshot == nil {
			return nil
		}
		out.Event = &v1.WatchLiveContentResponse_Snapshot{Snapshot: live(*e.Snapshot, generation)}
	case driver.ContentMessageStarted:
		out.Event = &v1.WatchLiveContentResponse_MessageStarted{MessageStarted: &v1.MessageStarted{MessageId: e.MessageID}}
	case driver.ContentMessageDelta:
		out.Event = &v1.WatchLiveContentResponse_MessageDelta{MessageDelta: &v1.MessageDelta{MessageId: e.MessageID, Text: e.Text, Truncated: e.Truncated}}
	case driver.ContentMessageReplaced:
		out.Event = &v1.WatchLiveContentResponse_MessageReplaced{MessageReplaced: &v1.MessageReplaced{MessageId: e.MessageID, Text: e.Text, Truncated: e.Truncated}}
	case driver.ContentMessageCompleted:
		out.Event = &v1.WatchLiveContentResponse_MessageCompleted{MessageCompleted: &v1.MessageCompleted{MessageId: e.MessageID}}
	case driver.ContentMessageSaved:
		out.Event = &v1.WatchLiveContentResponse_MessageSaved{MessageSaved: &v1.MessageSaved{MessageId: e.MessageID, NativeIds: e.NativeIDs}}
	case driver.ContentThoughtDelta:
		out.Event = &v1.WatchLiveContentResponse_ThoughtDelta{ThoughtDelta: &v1.ThoughtDelta{MessageId: e.MessageID, Text: e.Text}}
	case driver.ContentPromptSaved:
		out.Event = &v1.WatchLiveContentResponse_PromptSaved{PromptSaved: &v1.PromptSaved{Link: &v1.PromptLink{ClientMessageId: e.RequestID, NativeIds: e.NativeIDs}}}
	case driver.ContentActivity:
		out.Event = &v1.WatchLiveContentResponse_Activity{Activity: &v1.ActivityChanged{Activity: e.Activity}}
	case driver.ContentTurnSettled:
		out.Event = &v1.WatchLiveContentResponse_TurnSettled{TurnSettled: &v1.TurnSettled{}}
	default:
		if e.Tool == nil {
			return nil
		}
		phase := v1.ToolCallPhase_TOOL_CALL_PHASE_UPDATED
		switch e.Type {
		case driver.ContentToolStarted:
			phase = v1.ToolCallPhase_TOOL_CALL_PHASE_STARTED
		case driver.ContentToolCompleted:
			phase = v1.ToolCallPhase_TOOL_CALL_PHASE_COMPLETED
		case driver.ContentToolCleared:
			phase = v1.ToolCallPhase_TOOL_CALL_PHASE_CLEARED
		}
		out.Event = &v1.WatchLiveContentResponse_Tool{Tool: &v1.ToolCallUpdate{Phase: phase, Tool: tool(driver.HistoryTool{ID: e.Tool.ID, Name: e.Tool.Name, Input: e.Tool.Input, Output: e.Tool.Output, Status: e.Tool.Status})}}
	}
	return stream.Send(out)
}
