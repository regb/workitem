package daemonapi

import (
	"context"
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
	"github.com/regb/workitem/gen/wi/core/v1/corev1connect"
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
		out.Capabilities = &v1.AgentCapabilities{Control: channel, Prompt: c.Prompt, Steering: c.Steering, FollowUp: c.FollowUp, Cancel: c.Cancel, AtomicIdle: c.IdleGuard, LiveContent: c.LiveContent, ImageInput: c.ImageInput, LiveSettings: c.Settings, Answer: c.Answer}
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
	job := s.startJobKey(ctx, "start_agent", "start_agent:"+mode(req.Msg.Mode), m.ID, "", func(ctx context.Context, j *jobRun) error {
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
	job := s.startJobKey(ctx, "restart_agent", "restart_agent:"+fmt.Sprint(current.Generation), req.Msg.ItemId, "", func(ctx context.Context, j *jobRun) error {
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
	return s.controlLeaseStarting(ctx, itemID, leaseID, takeVacant, true)
}

// controlLeaseStarting is controlLease; start says whether taking vacant
// control may also start the agent. A caller that starts the agent itself,
// in a job that can ask for approvals, takes control without it.
func (s *Server) controlLeaseStarting(ctx context.Context, itemID, leaseID string, takeVacant, start bool) (*model.ControlLease, error) {
	lease, err := s.Local.Lease(ctx, itemID)
	if err != nil {
		return nil, err
	}
	who := caller(ctx)
	if lease != nil {
		// Command lines take control only to deliver what they send: one
		// takes over what another holds, as they used to share it, while a
		// browser tab or a terminal keeps what it holds.
		handover := takeVacant && leaseID == "" && lease.Holder == model.LeaseHolderCLI && who.holder == model.LeaseHolderCLI
		if (lease.ClientID != who.client || leaseID != "" && lease.ID != leaseID) && !handover {
			return nil, s.refuseControl(ctx, itemID, connect.CodePermissionDenied, "caller does not hold the control lease")
		}
		if lease.ClientID == who.client {
			return lease, nil
		}
		expected := lease.ID
		if _, err := s.Local.ReleaseLease(ctx, model.LeaseRequest{ItemID: itemID, Holder: lease.Holder, ClientID: lease.ClientID, ExpectedLeaseID: &expected}); err != nil {
			return nil, err
		}
	} else if !takeVacant {
		return nil, s.refuseControl(ctx, itemID, connect.CodeFailedPrecondition, "caller has no control lease")
	}
	// Taking vacant control may replace the agent with one this caller can
	// drive (a TUI with a headless one); the replacement must be started.
	result, err := s.App.AcquireControl(ctx, s.controlRequest(ctx, itemID, who.client, false, start))
	if err != nil {
		return nil, err
	}
	if result.Lease == nil {
		return nil, s.refuseControl(ctx, itemID, connect.CodeFailedPrecondition, "control lease was not granted")
	}
	s.rememberLease(result.Lease)
	return result.Lease, nil
}

// agentBusyMessage refuses a prompt to an agent at work.
const agentBusyMessage = "agent is busy; use steer or follow_up"

// agentBusy reports a prompt refused because the agent was at work.
func agentBusy(err error) bool {
	var refused *connect.Error
	return errors.As(err, &refused) && refused.Code() == connect.CodeFailedPrecondition && refused.Message() == agentBusyMessage
}

func (s *Server) send(ctx context.Context, r *v1.SendMessageRequest) error {
	sender := model.ActorFrom(ctx)
	// What another item's agent sends is recorded, so its envelope is kept
	// for retries and its history entry can name its sender.
	if r.ClientMessageId == "" && model.PrincipalItem(sender) != "" {
		r.ClientMessageId = "agent-" + id()
	}
	var receipt coordinator.APIMessageReceipt
	pending := false
	if r.ClientMessageId != "" {
		var err error
		receipt, pending, err = s.Core.Database().APIMessage(r.ItemId, r.ClientMessageId)
		if err != nil {
			return err
		}
		if pending {
			done, err := s.messageReceiptResult(receipt)
			if done || err != nil {
				return err
			}
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
			return precondition(agentBusyMessage)
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
	envelope := model.Envelope{Marker: receipt.Marker}
	if !pending {
		if envelope, err = s.messageEnvelope(ctx, r.ItemId, sender); err != nil {
			return err
		}
	} else if receipt.Marker != "" {
		if envelope, err = s.messageEnvelope(ctx, r.ItemId, receipt.Sender); err != nil {
			return err
		}
		envelope.Marker = receipt.Marker
	}
	text := r.Text
	if envelope.Marker != "" {
		text = envelope.Wrap(text)
	}
	commandID := ""
	if r.ClientMessageId != "" {
		commandID = messageCommandID(r.ItemId, r.ClientMessageId)
		if !pending {
			receipt = coordinator.APIMessageReceipt{ItemID: r.ItemId, MessageID: r.ClientMessageId, RuntimeID: runtime.Runtime.ID, Generation: current.Generation, CreatedAt: time.Now().UTC(), Sender: sender, Marker: envelope.Marker, AwaitsReply: r.AwaitsReply}
		}
		if receipt.RuntimeID != runtime.Runtime.ID {
			return aborted("message delivery belongs to another runtime")
		}
		if err := s.Core.Database().RecordAPIMessage(receipt); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
	}
	// Claude acknowledges queue acceptance before native submission.
	m, err := s.App.Store.LoadManifest(r.ItemId)
	if err != nil {
		return err
	}
	nativeConfirmation := m.PrimaryConversation != nil && m.PrimaryConversation.Driver == "claude"
	result, err := s.App.AgentControl(ctx, app.AgentControlOptions{ResolveOptions: resolve(r.ItemId), CommandType: delivery, Message: text, Images: images, Actor: controlActor(ctx), RequestID: r.ClientMessageId, CommandID: commandID, ExpectedRuntimeID: runtime.Runtime.ID, RequireIdle: delivery == "prompt" && runtime.Runtime.Capabilities.IdleGuard, ControllerHolder: lease.Holder, ControllerClientID: lease.ClientID, ControllerLeaseID: lease.ID})
	// A follow-up is deliberately scheduled after the current turn. The
	// driver's acknowledgement accepts that queue entry; waiting for native
	// submission here would turn a healthy, long-running turn into an error.
	// Startup prompts and steering still require native confirmation.
	if err == nil && nativeConfirmation && delivery != "follow_up" {
		err = s.waitMessageConfirmation(ctx, r.ItemId, runtime.Runtime.ID, result.Command.ID)
	}
	if r.ClientMessageId != "" {
		if err != nil {
			var refused *driver.ControlRejectedError
			if !nativeConfirmation && (errors.Is(err, driver.ErrNotIdle) || errors.As(err, &refused)) {
				_ = s.Core.Database().ForgetAPIMessage(r.ItemId, r.ClientMessageId)
			}
			return err
		}
		receipt.Accepted = true
		receipt.Delivered = !nativeConfirmation || delivery != "follow_up"
		if err := s.Core.Database().RecordAPIMessage(receipt); err != nil {
			return partial(err, r.ItemId, "accepted message")
		}
	}
	return err
}

// messageEnvelope says how a message from sender reaches itemID's agent: as
// typed when a person sent it, or the item's own agent; wrapped when another
// item's agent did, naming it and how it relates to the item.
func (s *Server) messageEnvelope(ctx context.Context, itemID, sender string) (model.Envelope, error) {
	from := model.PrincipalItem(sender)
	if from == "" || from == itemID {
		return model.Envelope{}, nil
	}
	source, err := s.canonical(ctx, from, 0)
	if err != nil {
		return model.Envelope{}, err
	}
	target, err := s.canonical(ctx, itemID, 0)
	if err != nil {
		return model.Envelope{}, err
	}
	relation := model.RelationOther
	switch {
	case target.Manifest.Owner == sender:
		relation = model.RelationOwner
	case source.Manifest.Owner == model.ItemPrincipal(itemID):
		relation = model.RelationDelegation
	}
	return model.Envelope{Marker: model.NewEnvelopeMarker(), From: sender, Title: source.Manifest.Title, Relation: relation}, nil
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
		if found {
			done, err := s.messageReceiptResult(receipt)
			if done || err != nil {
				return response(&v1.SendMessageResponse{Generation: receipt.Generation}, err)
			}
		}
	}
	if err := validateMessage(r); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.Text) == "" && len(r.Images) == 0 {
		return nil, invalid("message text or images are required")
	}
	// Saying something to an item that has no running place yet starts it,
	// with the message as its first prompt: one call for one intention.
	// Starting an active item's agent is what delivering a message means;
	// activating a backlog item is starting it, which takes control.
	if !r.RequireOnline {
		if current, err := s.canonical(ctx, r.ItemId, 0); err == nil && (current.Manifest.State == model.StateBacklog || current.Manifest.State == model.StateActive && current.Manifest.Checkout.Claim == nil) {
			if current.Manifest.State == model.StateBacklog {
				if err := s.authorize(ctx, corev1connect.LifecycleServiceStartItemProcedure, r.ItemId); err != nil {
					return nil, err
				}
			}
			started, err := s.startItem(ctx, r.ItemId, &v1.StartOptions{Prompt: r.Text, Images: r.Images, Mode: r.StartMode, ClientMessageId: r.ClientMessageId}, 0)
			if err != nil {
				return nil, err
			}
			return response(&v1.SendMessageResponse{Job: started.Job}, nil)
		}
	}
	m, err := s.requireActive(ctx, r.ItemId)
	if err != nil {
		return nil, rpcError(err)
	}
	if m.Checkout.Claim == nil {
		return nil, precondition("message requires a workspace claim")
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
	// Control is taken here without starting the agent: the job starts it,
	// which can take a while, such as setting up its checkout.
	if _, err := s.controlLeaseStarting(ctx, r.ItemId, r.LeaseId, true, false); err != nil {
		return nil, rpcError(err)
	}
	who := caller(ctx)
	job := s.startJobKey(ctx, "send_message", "send_message:"+r.ClientMessageId, r.ItemId, "", func(ctx context.Context, j *jobRun) error {
		ctx = context.WithValue(ctx, identityKey{}, who)
		unlock := s.lock(r.ItemId)
		defer unlock()
		if _, err := s.controlLeaseStarting(ctx, r.ItemId, r.LeaseId, true, false); err != nil {
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

// AnswerQuestion answers the agent's pending question through its driver.
// The caller takes vacant control, as when sending a message, but the agent
// must already be running: only a running agent asks.
func (s *Server) AnswerQuestion(ctx context.Context, req *connect.Request[v1.AnswerQuestionRequest]) (*connect.Response[v1.AnswerQuestionResponse], error) {
	r := req.Msg
	if len(r.Answers) == 0 {
		return nil, invalid("answers are required")
	}
	if len(r.ToolCallId) > 200 {
		return nil, invalid("tool_call_id is too long")
	}
	answer := agent.ControlCommand{Type: agent.CommandAnswer, ToolCallID: r.ToolCallId, Answers: r.Answers}
	if err := answer.ValidateContent(); err != nil {
		return nil, invalid(err.Error())
	}
	runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(r.ItemId))
	if err != nil {
		return nil, rpcError(err)
	}
	if runtime.Runtime == nil || !runtime.Online {
		return nil, precondition("agent is not running")
	}
	lease, err := s.controlLeaseStarting(ctx, r.ItemId, r.LeaseId, true, false)
	if err != nil {
		return nil, rpcError(err)
	}
	_, err = s.App.AgentControl(ctx, app.AgentControlOptions{ResolveOptions: resolve(r.ItemId), CommandType: agent.CommandAnswer, ToolCallID: r.ToolCallId, Answers: r.Answers, Actor: controlActor(ctx), ExpectedRuntimeID: runtime.Runtime.ID, ControllerHolder: lease.Holder, ControllerClientID: lease.ClientID, ControllerLeaseID: lease.ID})
	if err != nil {
		var refused *driver.ControlRejectedError
		if errors.As(err, &refused) {
			return nil, precondition(refused.Message)
		}
		return nil, rpcError(err)
	}
	return response(&v1.AnswerQuestionResponse{}, nil)
}
func (s *Server) AbortTurn(ctx context.Context, req *connect.Request[v1.AbortTurnRequest]) (*connect.Response[v1.AbortTurnResponse], error) {
	// Vacant control is taken, as for a prompt; the agent is not replaced.
	lease, err := s.controlLeaseStarting(ctx, req.Msg.ItemId, req.Msg.LeaseId, true, false)
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
	out := &v1.GetAgentSettingsResponse{AgentProfile: value.Agent, Driver: value.Driver, Values: value.Values, Overrides: value.Overrides, Running: value.Running, Catalog: modelCatalog(value.Catalog), CatalogError: value.CatalogError}
	return response(out, err)
}

// modelCatalog converts a driver's model catalog, if any.
func modelCatalog(c *driver.ModelCatalog) *v1.ModelCatalog {
	if c == nil {
		return nil
	}
	out := &v1.ModelCatalog{ReasoningLevels: c.ReasoningLevels, DefaultModel: c.Default, DefaultReasoning: c.DefaultReasoning}
	for _, m := range c.Models {
		out.Models = append(out.Models, &v1.ModelOption{Value: m.Value, Resolved: m.Resolved, Label: m.Label, Description: m.Description, Reasoning: m.Reasoning, Levels: m.Levels, ImageInput: m.ImageInput})
	}
	return out
}
func (s *Server) UpdateAgentSettings(ctx context.Context, req *connect.Request[v1.UpdateAgentSettingsRequest]) (*connect.Response[v1.UpdateAgentSettingsResponse], error) {
	opts := app.SetAgentSettingsOptions{ResolveOptions: resolve(req.Msg.ItemId), Settings: req.Msg.Settings, Restart: req.Msg.Restart}
	runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(req.Msg.ItemId))
	if err != nil {
		return nil, rpcError(err)
	}
	if runtime.Online {
		lease, err := s.controlLeaseStarting(ctx, req.Msg.ItemId, req.Msg.LeaseId, true, false)
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
	// A suggestion falls back to the description's first line when the
	// model fails; only no title at all is an error.
	title, err := s.App.GenerateTitle(ctx, description)
	if title != "" {
		err = nil
	}
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

// parts converts a message's parts, keeping their order.
func parts(values []driver.Part) []*v1.Part {
	out := make([]*v1.Part, 0, len(values))
	for _, p := range values {
		switch {
		case p.Thought != nil:
			out = append(out, &v1.Part{Kind: &v1.Part_Thought{Thought: &v1.Thought{Text: p.Thought.Text, DurationMs: uint32(max(0, min(p.Thought.DurationMS, int64(^uint32(0)))))}}})
		case p.Tool != nil:
			out = append(out, &v1.Part{Kind: &v1.Part_Tool{Tool: tool(*p.Tool)}})
		case p.Image != nil:
			out = append(out, &v1.Part{Kind: &v1.Part_Image{Image: historyImages([]driver.HistoryImage{*p.Image})[0]}})
		case p.Text != "":
			out = append(out, &v1.Part{Kind: &v1.Part_Text{Text: p.Text}})
		}
	}
	return out
}
func history(value driver.History) *v1.GetHistoryResponse {
	out := &v1.GetHistoryResponse{LeafId: value.LeafID, NextBefore: value.BeforeID, Truncated: value.Truncated, Warnings: value.Warnings, Messages: []*v1.ConversationMessage{}}
	for _, m := range value.Messages {
		timestamp, _ := time.Parse(time.RFC3339Nano, m.Timestamp)
		row := &v1.ConversationMessage{Id: m.ID, NativeIds: m.NativeIDs, Role: v1.MessageRole(v1.MessageRole_value["MESSAGE_ROLE_"+strings.ToUpper(m.Role)]), Parts: parts(m.Parts), Time: stamp(timestamp), Error: m.Error, UsageProvider: m.UsageProvider, Aborted: m.Aborted, Subagent: subagent(m.Subagent)}
		if m.Summary != nil {
			row.Summary = &v1.Summary{Kind: m.Summary.Kind, TokensBefore: uint32(max(0, m.Summary.TokensBefore))}
		}
		out.Messages = append(out.Messages, row)
	}
	return out
}
func (s *Server) GetHistory(ctx context.Context, req *connect.Request[v1.GetHistoryRequest]) (*connect.Response[v1.GetHistoryResponse], error) {
	value, err := s.App.ConversationHistoryPage(ctx, resolve(req.Msg.ItemId), req.Msg.LeafId, req.Msg.Before)
	if err != nil {
		return nil, rpcError(err)
	}
	out := history(value)
	if current, err := s.canonical(ctx, req.Msg.ItemId, 0); err == nil {
		s.linkPrompts(current.Manifest.ID, out.Messages)
	}
	return response(out, nil)
}

// linkPrompts marks each message a client sent with the ID it was sent
// with, from the links the agent reported: exact, unlike matching text.
func (s *Server) linkPrompts(itemID string, messages []*v1.ConversationMessage) {
	requests, err := s.Core.Database().PromptRequests(itemID)
	if err != nil || len(requests) == 0 {
		return
	}
	for _, m := range messages {
		for _, id := range append([]string{m.Id}, m.NativeIds...) {
			if request, ok := requests[id]; ok {
				m.ClientMessageId = request
				break
			}
		}
		if m.ClientMessageId == "" {
			continue
		}
		receipt, found, err := s.Core.Database().APIMessage(itemID, m.ClientMessageId)
		if err != nil || !found {
			continue
		}
		m.Sender = receipt.Sender
		if receipt.Marker == "" {
			continue
		}
		envelope := model.Envelope{Marker: receipt.Marker}
		for _, part := range m.Parts {
			if text, ok := part.Kind.(*v1.Part_Text); ok {
				text.Text, _ = envelope.Unwrap(text.Text)
			}
		}
	}
}
func (s *Server) GetSubagentHistory(ctx context.Context, req *connect.Request[v1.GetSubagentHistoryRequest]) (*connect.Response[v1.GetSubagentHistoryResponse], error) {
	value, err := s.App.SubagentHistoryPage(ctx, resolve(req.Msg.ItemId), req.Msg.SubagentId, req.Msg.Before)
	h := history(value)
	return response(&v1.GetSubagentHistoryResponse{Messages: h.Messages, NextBefore: h.NextBefore, Truncated: h.Truncated}, err)
}
func live(value driver.Content, generation uint32) *v1.LiveContent {
	out := &v1.LiveContent{Generation: generation, MessageId: value.MessageID, Parts: parts(value.Parts), Activity: value.Activity, SavedAs: value.SavedAs, Error: value.Error, Truncated: value.Truncated}
	for _, p := range value.Prompts {
		out.Prompts = append(out.Prompts, &v1.PromptLink{ClientMessageId: p.RequestID, NativeIds: p.NativeIDs})
	}
	if q := value.Question; q != nil {
		out.Question = tool(*q)
	}
	return out
}
func (s *Server) WatchLiveContent(ctx context.Context, req *connect.Request[v1.WatchLiveContentRequest], stream *connect.ServerStream[v1.WatchLiveContentResponse]) error {
	if err := s.authorize(ctx, corev1connect.AgentServiceWatchLiveContentProcedure, requestItem(req.Msg)); err != nil {
		return err
	}
	if _, err := s.canonical(ctx, req.Msg.ItemId, 0); err != nil {
		return rpcError(err)
	}
	return s.followContent(ctx, req.Msg.ItemId, stream.Send)
}

// contentPollInterval paces snapshot reads from a runtime that cannot
// stream its content.
const contentPollInterval = 400 * time.Millisecond

// followContent sends an item's live content until ctx ends: a snapshot,
// then the events after it. Without a running agent it sends an empty
// snapshot each second; a runtime that cannot stream is polled for
// snapshots, sent when they change.
func (s *Server) followContent(ctx context.Context, itemID string, send func(*v1.WatchLiveContentResponse) error) error {
	var previousGeneration uint32
	for {
		runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(itemID))
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
			if err := send(&v1.WatchLiveContentResponse{Cursor: fmt.Sprintf("%s:0", runtimeID), Event: &v1.WatchLiveContentResponse_Snapshot{Snapshot: &v1.LiveContent{Generation: generation}}}); err != nil {
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
		err = s.App.AgentContentStream(ctx, resolve(itemID), func(event driver.ContentEvent) error {
			// Native streams always begin with a snapshot. Sending a normalized snapshot
			// per change also safely resets cursors that cannot be replayed by the driver.
			return s.sendContentEvent(send, runtimeID, generation, event)
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, driver.ErrContentStreamUnsupported) {
			err = s.pollContent(ctx, itemID, runtimeID, generation, send)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		current, e := s.App.AgentRuntimeStatus(ctx, resolve(itemID))
		if e != nil {
			return rpcError(e)
		}
		if current.Runtime != nil && current.Runtime.Generation == previousGeneration && current.Online {
			return rpcError(err)
		}
	}
}

// pollContent sends snapshots of a runtime that cannot stream, when they
// change, until a read fails, as it does once the runtime stops.
func (s *Server) pollContent(ctx context.Context, itemID, runtimeID string, generation uint32, send func(*v1.WatchLiveContentResponse) error) error {
	var last *v1.LiveContent
	for sequence := uint64(1); ; sequence++ {
		value, err := s.App.AgentContent(ctx, resolve(itemID))
		if err != nil {
			return err
		}
		snapshot := live(value, generation)
		if last == nil || !proto.Equal(snapshot, last) {
			last = snapshot
			if err := send(&v1.WatchLiveContentResponse{Cursor: base64.RawURLEncoding.EncodeToString([]byte(runtimeID + ":" + strconv.FormatUint(sequence, 10))), Event: &v1.WatchLiveContentResponse_Snapshot{Snapshot: snapshot}}); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(contentPollInterval):
		}
	}
}
func (s *Server) sendContentEvent(send func(*v1.WatchLiveContentResponse) error, runtimeID string, generation uint32, e driver.ContentEvent) error {
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
	case driver.ContentQuestion:
		changed := &v1.QuestionChanged{}
		if q := e.Tool; q != nil {
			changed.Question = tool(driver.HistoryTool{ID: q.ID, Name: q.Name, Status: q.Status, Input: q.Input})
		}
		out.Event = &v1.WatchLiveContentResponse_Question{Question: changed}
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
	return send(out)
}
