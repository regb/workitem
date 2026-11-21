package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

type headlessDelivery struct {
	started                 bool
	command                 protocol.ControlCommand
	digest, nativeID, phase string
	err                     error
	deadline                time.Time
}
type headlessControls struct {
	inputBroken                       bool
	nativeKnown                       map[string]*headlessDelivery
	session                           string
	input                             *os.File
	known                             map[string]*headlessDelivery
	work                              map[string]*headlessDelivery
	queue                             []*headlessDelivery
	aborting                          *headlessDelivery
	settings                          map[string]*headlessDelivery
	reported                          *settingsReport
	initialized, stopping, nativeBusy bool
	cancelQueued                      bool
	// question is the AskUserQuestion call Claude waits on, held open
	// until CommandAnswer answers it.
	question *headlessQuestion
	// answered is the last question answered, so a repeated answer to it
	// succeeds without reaching Claude again.
	answered string
	// showQuestion publishes the held question's tool call and input as
	// live content; an empty id clears it.
	showQuestion func(id, input string)
	emit         func(protocol.RuntimeEvent)
}

// headlessQuestion is Claude's can_use_tool request for AskUserQuestion.
type headlessQuestion struct {
	requestID, toolUseID string
	input                map[string]any
}

func (s *headlessControls) busy() bool { return s.nativeBusy || len(s.work) > 0 || s.aborting != nil }
func (s *headlessControls) fact(kind string, d *headlessDelivery, failed bool, state, reason string) {
	s.emit(protocol.RuntimeEvent{Type: kind, CommandID: d.command.ID, RequestID: d.command.RequestID, Failed: failed, DeliveryState: state, ReasonCode: reason, UsageProvider: reasonUsageProvider(reason)})
}
func (s *headlessControls) remember(c protocol.ControlCommand) (*headlessDelivery, bool, error) {
	if c.ID == "" || len(c.ID) > 200 || len(c.RequestID) > 200 {
		return nil, false, fmt.Errorf("bounded command identity required")
	}
	if s.known == nil {
		s.known = map[string]*headlessDelivery{}
		s.work = map[string]*headlessDelivery{}
		s.nativeKnown = map[string]*headlessDelivery{}
	}
	if d := s.known[c.ID]; d != nil {
		if d.digest != commandDigest(c) || d.command.Type != c.Type || d.command.RequestID != c.RequestID || d.command.RequireIdle != c.RequireIdle {
			return nil, false, fmt.Errorf("command identity reused with different content")
		}
		return d, true, d.err
	}
	if len(s.known) >= 1024 {
		return nil, false, fmt.Errorf("Claude command deduplication capacity reached; restart the runtime")
	}
	// Retain only bounded delivery fields, not the caller's unused metadata.
	stored := protocol.ControlCommand{ID: c.ID, RequestID: c.RequestID, Type: c.Type, Message: c.Message, Images: c.Images, RequireIdle: c.RequireIdle}
	d := &headlessDelivery{command: stored, digest: commandDigest(c), nativeID: uuid("headless-command:" + c.ID)}
	s.known[c.ID] = d
	s.nativeKnown[d.nativeID] = d
	return d, false, nil
}
func (s *headlessControls) accept(c protocol.ControlCommand) error {
	c.Message = strings.TrimSpace(c.Message)
	if err := c.ValidateContent(); err != nil {
		return err
	}
	if c.RequireIdle {
		return fmt.Errorf("Claude does not support atomic idle-only controls")
	}
	if s.stopping {
		return fmt.Errorf("Claude is stopping")
	}
	switch c.Type {
	case protocol.CommandSet:
		return s.applySetting(c)
	case protocol.CommandAnswer:
		return s.answer(c)
	case protocol.CommandPrompt, protocol.CommandSteer, protocol.CommandFollowUp, protocol.CommandAbort:
	default:
		return fmt.Errorf("unsupported Claude headless control")
	}
	if c.Type != protocol.CommandAbort && strings.TrimSpace(c.Message) == "" && len(c.Images) == 0 {
		return fmt.Errorf("text or images required")
	}
	if c.Type == protocol.CommandAbort && (c.Message != "" || len(c.Images) > 0) {
		return fmt.Errorf("abort cannot contain input")
	}
	d, duplicate, err := s.remember(c)
	if duplicate || err != nil {
		return err
	}
	reject := func(err error) error {
		d.phase = "not_sent"
		d.err = err
		d.command.Message = ""
		d.command.Images = nil
		s.fact(protocol.EventCommandRejected, d, true, "not_sent", "delivery_failed")
		return err
	}
	if c.Type == protocol.CommandAbort {
		if s.aborting != nil {
			return reject(fmt.Errorf("Claude abort is awaiting confirmation"))
		}
		s.cancelOwnQueue()
		if s.inputBroken {
			return reject(fmt.Errorf("Claude native input is unavailable; restart before further control delivery"))
		}
		if !s.busy() {
			d.phase = "confirmed"
			s.fact(protocol.EventCommandAccepted, d, false, "confirmed", protocol.ReasonAbortAccepted)
			return nil
		}
		if !s.initialized {
			return reject(fmt.Errorf("Claude control protocol is not ready"))
		}
		if len(s.work) > 1 && !s.cancelQueued {
			return reject(fmt.Errorf("Claude cannot confirm cancellation of submitted queued messages"))
		}
		request := headlessControl(d.nativeID, "interrupt")
		if s.cancelQueued {
			request["request"].(map[string]any)["cancel_queued"] = true
		}
		d.phase = "submitted"
		d.deadline = time.Now().Add(15 * time.Second)
		s.aborting = d
		if _, err := writeHeadlessFrame(s.input, request); err != nil {
			s.inputBroken = true
			d.phase = "uncertain"
			d.err = fmt.Errorf("Claude abort delivery uncertain; do not repeat automatically")
			s.fact(protocol.EventCommandRejected, d, true, "uncertain", "abort_uncertain")
			return d.err
		}
		s.fact(protocol.EventQueueChanged, d, false, "submitted", "command_submitted")
		return nil
	}
	if s.aborting != nil {
		return reject(fmt.Errorf("Claude abort is awaiting confirmation"))
	}
	if s.inputBroken {
		return reject(fmt.Errorf("Claude native input is unavailable; restart before further control delivery"))
	}
	for _, pending := range s.work {
		if pending.phase == "uncertain" {
			return reject(fmt.Errorf("Claude input delivery is uncertain; no further input sent"))
		}
	}
	if len(s.work)+len(s.queue) >= 32 {
		return reject(fmt.Errorf("Claude pending input limit reached"))
	}
	if c.Type == protocol.CommandPrompt && s.busy() {
		return reject(fmt.Errorf("Claude is busy; use steer or follow-up"))
	}
	if !s.initialized || len(s.queue) > 0 || c.Type == protocol.CommandFollowUp && s.busy() {
		if len(s.queue) >= 32 {
			return reject(fmt.Errorf("Claude follow-up queue is full"))
		}
		d.phase = "queued"
		s.queue = append(s.queue, d)
		reason := "queued_follow_up"
		if !s.initialized {
			reason = "queued_startup"
		}
		s.fact(protocol.EventQueueChanged, d, false, "queued", reason)
		return nil
	}
	return s.submit(d)
}
func (s *headlessControls) submit(d *headlessDelivery) error {
	blocks := []any{}
	if d.command.Message != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": d.command.Message})
	}
	for _, img := range d.command.Images {
		blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": img.MIMEType, "data": img.Data}})
	}
	priority := "later"
	if d.command.Type == protocol.CommandSteer {
		priority = "next"
	}
	message := map[string]any{"type": "user", "uuid": d.nativeID, "session_id": s.session, "message": map[string]any{"role": "user", "content": blocks}, "parent_tool_use_id": nil, "origin": map[string]any{"kind": "human"}, "client_composed": true, "priority": priority}
	s.work[d.nativeID] = d
	d.phase = "submitted"
	d.deadline = time.Now().Add(15 * time.Second)
	n, err := writeHeadlessFrame(s.input, message)
	d.command.Message = ""
	d.command.Images = nil
	if err != nil {
		s.inputBroken = true
		d.err = err
		if n == 0 {
			delete(s.work, d.nativeID)
			d.phase = "not_sent"
			s.fact(protocol.EventCommandRejected, d, true, "not_sent", "delivery_failed")
		} else {
			d.phase = "uncertain"
			d.err = fmt.Errorf("Claude prompt delivery uncertain; do not resend automatically")
			s.fact(protocol.EventCommandRejected, d, true, "uncertain", "submission_uncertain")
		}
		return d.err
	}
	s.fact(protocol.EventQueueChanged, d, false, "submitted", "command_submitted")
	return nil
}
func (s *headlessControls) confirm(id string) {
	if d := s.work[id]; d != nil && (d.phase == "submitted" || d.phase == "uncertain") {
		d.phase = "confirmed"
		d.err = nil
		d.deadline = time.Time{}
		s.fact(protocol.EventCommandAccepted, d, false, "confirmed", "native_confirmed")
	}
}
func (s *headlessControls) result(f headlessFrame) (bool, bool) {
	failed := f.IsError || f.Subtype != "success"
	reason := ""
	switch f.TerminalReason {
	case "interrupted", "cancelled", "aborted_streaming", "aborted_tools":
		// The operator stopped the turn: it ended, without failing.
		failed = false
	case "api_error", "model_error":
		failed, reason = true, "native_api_failed"
	case "", "completed", "tool_deferred", "background_requested", "hook_stopped", "stop_hook_prevented":
		// These can end a successful native loop.
	default:
		// Unknown terminal reasons cannot establish successful completion.
		failed = true
	}
	if f.APIErrorStatus >= 400 {
		failed, reason = true, "native_api_failed"
		switch f.APIErrorStatus {
		case 401, 403:
			reason = "native_auth_failed"
		case 429:
			reason = "usage_limit"
		}
	}
	if f.NumTurns == 0 && strings.HasPrefix(f.Result, "UserPromptSubmit operation blocked by hook:") {
		failed = true
		reason = "prompt_blocked"
	}
	ids := append([]string{}, f.UserIDs...)
	if f.UserID != "" {
		ids = append(ids, f.UserID)
	}
	seen := map[string]bool{}
	matched := false
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if d := s.work[id]; d != nil {
			matched = true
			s.confirm(id)
			d.phase = "completed"
			delete(s.work, id)
			s.fact(protocol.EventAgentSettled, d, failed, "", reason)
		}
	}
	// Background turns have no Wi command identity. A late result for a completed
	// Wi command must not release a newer reservation or settle its observation.
	if !matched && len(s.work) == 0 {
		s.emit(protocol.RuntimeEvent{Type: protocol.EventAgentSettled, Failed: failed, ReasonCode: reason, UsageProvider: reasonUsageProvider(reason)})
	}
	if len(s.work) == 0 {
		s.nativeBusy = false
		// A fresh native settlement can release the interruption fence without
		// claiming that the missing interrupt receipt ever arrived.
		if s.aborting != nil && s.aborting.phase == "uncertain" {
			s.aborting = nil
		}
	}
	return matched, failed
}
func (s *headlessControls) retiredFrame(f headlessFrame) bool {
	ids := append([]string{}, f.UserIDs...)
	if f.UserID != "" {
		ids = append(ids, f.UserID)
	}
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		d := s.nativeKnown[id]
		if d == nil || s.work[id] != nil {
			return false
		}
	}
	return true
}
func (s *headlessControls) interruptResponse(f headlessFrame) {
	d := s.aborting
	if d == nil || f.Response.RequestID != d.nativeID {
		return
	}
	if f.Response.Subtype == "success" && (f.Response.Response.Queued == nil ||
		s.cancelQueued && (f.Response.Response.Cancelled == nil || len(f.Response.Response.Queued) != 0)) {
		// Old or incompatible CLIs can return an empty success payload. That is
		// not a receipt. Keep the interruption fence until fresh settlement.
		d.phase = "uncertain"
		d.deadline = time.Time{}
		d.err = fmt.Errorf("Claude interrupt receipt is incomplete; do not repeat automatically")
		s.fact(protocol.EventCommandRejected, d, true, "uncertain", "abort_uncertain")
		return
	}
	s.aborting = nil
	if f.Response.Subtype != "success" {
		d.phase = "failed"
		d.err = fmt.Errorf("Claude rejected interrupt")
		s.fact(protocol.EventCommandRejected, d, true, "uncertain", "abort_uncertain")
		return
	}
	for _, id := range f.Response.Response.Cancelled {
		if pending := s.work[id]; pending != nil {
			delete(s.work, id)
			pending.phase = "cancelled"
			pending.err = fmt.Errorf("Claude cancelled queued input")
			s.fact(protocol.EventCommandRejected, pending, true, "cancelled", "native_queued_cancelled")
		}
	}
	d.phase = "confirmed"
	d.err = nil
	s.fact(protocol.EventCommandAccepted, d, false, "confirmed", protocol.ReasonAbortAccepted)
}
func (s *headlessControls) cancelOwnQueue() {
	for _, d := range s.queue {
		d.phase = "cancelled"
		d.err = fmt.Errorf("queued Claude command cancelled before delivery")
		d.command.Message = ""
		d.command.Images = nil
		s.fact(protocol.EventCommandRejected, d, true, "cancelled", "queued_cancelled")
	}
	s.queue = nil
}
func (s *headlessControls) tick(now time.Time) {
	for _, d := range s.work {
		if !d.deadline.IsZero() && !now.Before(d.deadline) {
			d.deadline = time.Time{}
			d.phase = "uncertain"
			d.err = fmt.Errorf("Claude receipt was not confirmed; do not resend automatically")
			s.fact(protocol.EventCommandRejected, d, true, "uncertain", "confirmation_timeout")
		}
	}
	if d := s.aborting; d != nil && !d.deadline.IsZero() && !now.Before(d.deadline) {
		d.deadline = time.Time{}
		d.phase = "uncertain"
		d.err = fmt.Errorf("Claude abort was not confirmed; do not repeat automatically")
		s.fact(protocol.EventCommandRejected, d, true, "uncertain", "abort_timeout")
	}
	if s.initialized && !s.stopping && !s.inputBroken && !s.busy() && len(s.queue) > 0 {
		d := s.queue[0]
		s.queue = s.queue[1:]
		_ = s.submit(d)
	}
}
func (s *headlessControls) close() {
	s.stopping = true
	s.cancelOwnQueue()
	for _, d := range s.work {
		d.phase = "uncertain"
		d.err = fmt.Errorf("Claude runtime stopped before turn completion")
		s.fact(protocol.EventCommandRejected, d, true, "uncertain", "runtime_stopped")
	}
	s.work = nil
	if d := s.aborting; d != nil {
		s.fact(protocol.EventCommandRejected, d, true, "uncertain", "runtime_stopped")
		s.aborting = nil
	}
}

// reasoningLevels are the reasoning levels Claude accepts, as its --effort
// values.
var reasoningLevels = []string{"low", "medium", "high", "xhigh", "max"}

// applySetting changes the model or reasoning level of the running session through
// Claude's control requests. The control response confirms or rejects it.
func (s *headlessControls) applySetting(c protocol.ControlCommand) error {
	if !s.initialized {
		return fmt.Errorf("Claude control protocol is not ready")
	}
	if s.inputBroken {
		return fmt.Errorf("Claude native input is unavailable; restart before further control delivery")
	}
	var request map[string]any
	switch c.Setting.Name {
	case "model":
		request = headlessControl("", "set_model")
		request["request"].(map[string]any)["model"] = c.Setting.Value
	case protocol.SettingReasoning:
		if !slices.Contains(reasoningLevels, c.Setting.Value) {
			return fmt.Errorf("invalid Claude reasoning level %q", c.Setting.Value)
		}
		request = headlessControl("", "apply_flag_settings")
		request["request"].(map[string]any)["settings"] = map[string]any{"effortLevel": c.Setting.Value}
	default:
		return fmt.Errorf("Claude cannot change %s while running", c.Setting.Name)
	}
	d, duplicate, err := s.remember(c)
	if duplicate || err != nil {
		return err
	}
	request["request_id"] = d.nativeID
	if s.settings == nil {
		s.settings = map[string]*headlessDelivery{}
	}
	s.settings[d.nativeID] = d
	d.phase = "submitted"
	if _, err := writeHeadlessFrame(s.input, request); err != nil {
		delete(s.settings, d.nativeID)
		s.inputBroken = true
		d.phase = "not_sent"
		d.err = err
		s.fact(protocol.EventCommandRejected, d, true, "not_sent", "delivery_failed")
		return err
	}
	return nil
}

// settingResponse settles a setting change from its control response.
func (s *headlessControls) settingResponse(f headlessFrame) {
	d := s.settings[f.Response.RequestID]
	if d == nil {
		return
	}
	delete(s.settings, f.Response.RequestID)
	if f.Response.Subtype != "success" {
		d.phase = "failed"
		d.err = fmt.Errorf("Claude rejected the setting: %s", f.Response.Error)
		s.fact(protocol.EventCommandRejected, d, true, "", protocol.ReasonSettingRejected)
		return
	}
	d.phase = "confirmed"
	s.fact(protocol.EventCommandAccepted, d, false, "confirmed", protocol.ReasonSettingApplied)
	if s.reported != nil && d.command.Setting != nil {
		s.reported.report(d.command.Setting.Name, d.command.Setting.Value)
	}
}

// ask holds Claude's request to use AskUserQuestion until the operator
// answers through CommandAnswer. Other tools are not held.
func (s *headlessControls) ask(f headlessFrame) bool {
	if f.Request.ToolName != "AskUserQuestion" || f.RequestID == "" || len(f.RequestID) > 200 || len(f.Request.ToolUseID) > 200 {
		return false
	}
	var input map[string]any
	if json.Unmarshal(f.Request.Input, &input) != nil {
		return false
	}
	if _, ok := input["questions"].([]any); !ok {
		return false
	}
	s.question = &headlessQuestion{requestID: f.RequestID, toolUseID: f.Request.ToolUseID, input: input}
	if s.showQuestion != nil {
		s.showQuestion(f.Request.ToolUseID, string(f.Request.Input))
	}
	return true
}

// dropQuestion forgets the held question, which Claude withdrew or whose
// turn ended.
func (s *headlessControls) dropQuestion() {
	if s.question != nil && s.showQuestion != nil {
		s.showQuestion("", "")
	}
	s.question = nil
}

// answer answers the held question: Claude runs AskUserQuestion with the
// answers in its input, as when the user answers its dialog.
func (s *headlessControls) answer(c protocol.ControlCommand) error {
	q := s.question
	if q == nil && c.ToolCallID != "" && c.ToolCallID == s.answered {
		return nil
	}
	if q == nil {
		return fmt.Errorf("Claude is not waiting for an answer")
	}
	if c.ToolCallID != "" && c.ToolCallID != q.toolUseID {
		return fmt.Errorf("Claude is waiting on another question")
	}
	if err := matchAnswers(q.input, c.Answers); err != nil {
		return err
	}
	input := make(map[string]any, len(q.input)+1)
	for key, value := range q.input {
		input[key] = value
	}
	input["answers"] = c.Answers
	response := map[string]any{"subtype": "success", "request_id": q.requestID, "response": map[string]any{"behavior": "allow", "updatedInput": input}}
	if _, err := writeHeadlessFrame(s.input, map[string]any{"type": "control_response", "response": response}); err != nil {
		s.inputBroken = true
		return fmt.Errorf("Claude answer delivery uncertain: %w", err)
	}
	s.dropQuestion()
	s.answered = q.toolUseID
	return nil
}

// matchAnswers checks that answers answer the questions asked: one answer
// per question text, for every question.
func matchAnswers(input map[string]any, answers map[string]string) error {
	questions, _ := input["questions"].([]any)
	asked := map[string]bool{}
	for _, value := range questions {
		if question, ok := value.(map[string]any); ok {
			if text, ok := question["question"].(string); ok {
				asked[text] = true
			}
		}
	}
	for text := range answers {
		if !asked[text] {
			return fmt.Errorf("Claude did not ask %q", text)
		}
	}
	if len(answers) != len(asked) {
		return fmt.Errorf("answer each of Claude's %d questions", len(asked))
	}
	return nil
}
