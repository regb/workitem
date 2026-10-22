package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

type commandDelivery struct {
	command        protocol.ControlCommand
	digest, phase  string
	identityDigest string
	cleanup        func()
	err            error
	reasonCode     string
	deadline       time.Time
}
type tuiControls struct {
	input                 tuiInput
	ready, busy, stopping bool
	sessionStarted        bool
	settledAt             time.Time
	inputReadyAt          time.Time
	lastFailureCode       string
	executionPending      bool
	// asking names the dialog in which Claude waits for the user's answer
	// mid-turn: a question, a plan approval or a permission prompt.
	asking   string
	pending  *commandDelivery
	aborting *commandDelivery
	queue    []*commandDelivery
	seen     map[string]*commandDelivery
	emit     func(string, protocol.ControlCommand, bool)
}

func (d *commandDelivery) releaseImages() {
	if d.cleanup != nil {
		d.cleanup()
		d.cleanup = nil
	}
}
func commandDigest(c protocol.ControlCommand) string {
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(c.Message)))
	for _, image := range c.Images {
		h.Write([]byte{0})
		h.Write([]byte(image.MIMEType))
		h.Write([]byte{0})
		h.Write([]byte(image.Data))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func promptDigest(message string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(message)))
	return hex.EncodeToString(sum[:])
}
func (s *tuiControls) accept(ctx context.Context, c protocol.ControlCommand) error {
	if err := c.ValidateContent(); err != nil {
		return err
	}
	if s.input == nil {
		return fmt.Errorf("Claude TUI controls require its owned tmux pane")
	}
	if s.stopping {
		return fmt.Errorf("Claude is stopping")
	}
	if c.ID == "" || len(c.ID) > 200 || len(c.RequestID) > 200 {
		return fmt.Errorf("bounded command identity required")
	}
	if len(c.Images) > 0 {
		if _, ok := s.input.(imageTUIInput); !ok {
			return fmt.Errorf("Claude TUI image input is not supported")
		}
		if err := validateImages(c.Images); err != nil {
			return err
		}
	}
	if previous := s.seen[c.ID]; previous != nil {
		if previous.command.Type != c.Type || previous.identityDigest != commandDigest(c) || previous.command.RequestID != c.RequestID || previous.command.RequireIdle != c.RequireIdle {
			return fmt.Errorf("command identity reused with different content")
		}
		return previous.err
	}
	if !supportsControl(s.input, c.Type) {
		return fmt.Errorf("Claude %s keybinding is unavailable or cannot be delivered safely", c.Type)
	}
	if c.Type == protocol.CommandAbort {
		return s.abort(ctx, c)
	}
	if s.aborting != nil {
		return fmt.Errorf("Claude abort is awaiting settlement confirmation")
	}
	if !utf8.ValidString(c.Message) || strings.TrimSpace(c.Message) == "" && len(c.Images) == 0 || len(c.Message) > 64<<10 {
		return fmt.Errorf("Claude prompt must contain text or images, with at most 65536 UTF-8 text bytes")
	}
	for _, r := range c.Message {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return fmt.Errorf("Claude prompt contains terminal control characters")
		}
	}
	if len(s.seen) >= 1024 {
		return fmt.Errorf("Claude command deduplication capacity reached; restart the runtime")
	}
	if len(s.queue) >= 32 {
		return fmt.Errorf("Claude follow-up queue is full")
	}
	if c.RequireIdle && (!s.ready || time.Now().Before(s.inputReadyAt) || s.busy || s.pending != nil || len(s.queue) > 0) {
		return fmt.Errorf("Claude is not observed idle")
	}
	if c.Type == protocol.CommandPrompt && s.ready && (s.busy || s.pending != nil || len(s.queue) > 0) {
		return fmt.Errorf("Claude is busy; use steer or follow-up")
	}
	if c.Type == protocol.CommandSteer && s.pending != nil {
		return fmt.Errorf("Claude is awaiting submission confirmation")
	}
	d := &commandDelivery{command: c, digest: promptDigest(c.Message), identityDigest: commandDigest(c), phase: "queued"}
	if s.seen == nil {
		s.seen = map[string]*commandDelivery{}
	}
	s.seen[c.ID] = d
	if c.Type == protocol.CommandFollowUp || !s.ready || time.Now().Before(s.inputReadyAt) {
		s.queue = append(s.queue, d)
		s.emit(protocol.EventQueueChanged, c, false)
		return nil
	}
	return s.submit(ctx, d, c.Type == protocol.CommandSteer && s.busy)
}
func (s *tuiControls) abort(ctx context.Context, c protocol.ControlCommand) error {
	if c.Message != "" {
		return fmt.Errorf("Claude abort does not accept message content")
	}
	if s.aborting != nil {
		return fmt.Errorf("Claude abort is already awaiting confirmation")
	}
	if len(s.seen) >= 1024 {
		return fmt.Errorf("Claude command deduplication capacity reached; restart the runtime")
	}
	if c.RequireIdle && (!s.ready || s.busy || s.pending != nil || len(s.queue) > 0) {
		return fmt.Errorf("Claude is not observed idle")
	}
	// Unknown readiness without an in-flight submission is not safe for an
	// interrupt shortcut.
	if !s.ready && (s.busy || s.pending != nil) {
		return fmt.Errorf("Claude readiness is unknown; cannot safely interrupt")
	}
	d := &commandDelivery{command: c, digest: promptDigest(c.Message), identityDigest: commandDigest(c), phase: "submitted", deadline: time.Now().Add(15 * time.Second)}
	if s.seen == nil {
		s.seen = map[string]*commandDelivery{}
	}
	s.seen[c.ID] = d
	active := s.busy || s.pending != nil
	s.cancelQueued("Claude command cancelled by abort")
	if s.pending != nil {
		pending := s.pending
		pending.err = fmt.Errorf("Claude prompt interrupted before submission confirmation")
		pending.phase = "uncertain"
		pending.reasonCode = "submission_interrupted"
		s.emit(protocol.EventCommandRejected, pending.command, true)
		s.pending = nil
	}
	if !active {
		d.phase = "confirmed"
		s.emit(protocol.EventCommandAccepted, c, false)
		s.emit(protocol.EventAgentSettled, c, false)
		return nil
	}
	s.aborting = d
	if err := s.input.Interrupt(ctx); err != nil {
		d.err = fmt.Errorf("queued work cancelled; abort not confirmed: %w", err)
		d.reasonCode = "abort_uncertain"
		d.phase = "uncertain"
		s.emit(protocol.EventCommandRejected, c, true)
		return d.err
	}
	s.emit(protocol.EventQueueChanged, d.command, false)
	return nil
}
func (s *tuiControls) cancelQueued(reason string) {
	for _, d := range s.queue {
		d.err = fmt.Errorf("%s", reason)
		d.phase = "cancelled"
		d.command.Message = ""
		d.command.Images = nil
		d.releaseImages()
		s.emit(protocol.EventCommandRejected, d.command, true)
	}
	s.queue = nil
}
func (s *tuiControls) settled() {
	s.lastFailureCode = ""
	s.asking = ""
	s.ready = s.sessionStarted
	s.busy = false
	s.executionPending = false
	s.settledAt = time.Now()
	if s.aborting != nil {
		d := s.aborting
		s.aborting = nil
		d.phase = "confirmed"
		s.emit(protocol.EventCommandAccepted, d.command, false)
	}
}

func (s *tuiControls) submit(ctx context.Context, d *commandDelivery, steer bool) error {
	s.pending = d
	d.phase = "submitted"
	d.deadline = time.Now().Add(15 * time.Second)
	var err error
	if len(d.command.Images) > 0 {
		var digest string
		digest, d.cleanup, err = s.input.(imageTUIInput).SendImages(ctx, d.command.Message, d.command.Images, steer)
		if digest != "" {
			d.digest = digest
		}
	} else {
		err = s.input.Send(ctx, d.command.Message, steer)
	}
	d.command.Message = ""
	d.command.Images = nil
	if err != nil {
		d.err = err
		d.phase = "uncertain"
		var untouched beforeInputError
		if errors.As(err, &untouched) {
			d.phase = "failed"
			d.releaseImages()
			s.pending = nil
			s.ready = false
			s.lastFailureCode = failureCode(err)
		}
		s.emit(protocol.EventCommandRejected, d.command, true)
	} else {
		s.emit(protocol.EventQueueChanged, d.command, false)
	}
	// Even uncertain delivery reserves the slot until a hook confirms acceptance
	// or its deadline expires. Retrying this command ID never retypes anything.
	return err
}
func (s *tuiControls) hook(event, digest string) {
	switch event {
	case "SessionStart":
		s.lastFailureCode = ""
		// The hook can precede React's first editor render. Queue without blocking
		// the event loop so intervening busy/stopping hooks still take precedence.
		s.inputReadyAt = time.Now().Add(500 * time.Millisecond)
		s.ready = true
		s.sessionStarted = true
		if !s.busy {
			s.settledAt = time.Now()
		}
	case "UserPromptSubmit":
		s.executionPending = true
		s.lastFailureCode = ""
		s.inputReadyAt = time.Time{}
		s.busy = true
		if s.pending != nil && digest != "" && digest == s.pending.digest {
			d := s.pending
			s.pending = nil
			d.phase = "confirmed"
			d.releaseImages()
			s.emit(protocol.EventCommandAccepted, d.command, false)
		}
	case "PreToolUse":
		s.executionPending = false
		s.lastFailureCode = ""
		s.inputReadyAt = time.Time{}
		s.busy = true
	case "Stop", "StopFailure":
		s.settled()
	case "SessionEnd":
		s.stopping = true
	}
}

// observeQuestion tracks whether Claude shows a dialog waiting for the user.
// PreToolUse precedes the permission check, so a later permission
// notification for the same tool still applies; any further progress in the
// turn means the dialog was answered.
func (s *tuiControls) observeQuestion(event, tool, notification string) {
	switch event {
	case "PreToolUse":
		s.asking = questionTools[tool]
	case "Notification":
		if reason := questionNotifications[notification]; reason != "" {
			s.asking = reason
		}
	case "PostToolUse", "PostToolUseFailure", "UserPromptSubmit", "Stop", "StopFailure", "SessionStart", "SessionEnd":
		s.asking = ""
	}
}

var questionTools = map[string]string{"AskUserQuestion": "question_pending", "ExitPlanMode": "plan_approval_pending"}

var questionNotifications = map[string]string{"permission_prompt": "permission_pending", "elicitation_dialog": "question_pending"}

func (s *tuiControls) tick(ctx context.Context, now time.Time) {
	if s.aborting != nil && now.After(s.aborting.deadline) {
		d := s.aborting
		s.aborting = nil
		d.phase = "uncertain"
		d.err = fmt.Errorf("Claude abort was not confirmed; do not resend automatically")
		d.reasonCode = "abort_timeout"
		s.lastFailureCode = d.reasonCode
		s.emit(protocol.EventCommandRejected, d.command, true)
		s.ready = false
	}
	if s.pending != nil && now.After(s.pending.deadline) {
		d := s.pending
		s.pending = nil
		d.phase = "uncertain"
		d.err = fmt.Errorf("Claude submission was not confirmed; delivery uncertain, do not resend")
		d.reasonCode = "confirmation_timeout"
		s.lastFailureCode = d.reasonCode
		s.emit(protocol.EventCommandRejected, d.command, true)
		// Missing hooks do not prove that input was never submitted. Stop draining
		// the queue until a later lifecycle hook establishes idle again.
		s.ready = false
	}
	if s.stopping || !s.ready || now.Before(s.inputReadyAt) || s.busy || s.pending != nil || s.aborting != nil || len(s.queue) == 0 || now.Sub(s.settledAt) < 500*time.Millisecond {
		return
	}
	d := s.queue[0]
	s.queue = s.queue[1:]
	_ = s.submit(ctx, d, false)
}
func (s *tuiControls) close() {
	defer func() {
		for _, d := range s.seen {
			d.releaseImages()
		}
	}()
	s.stopping = true
	if s.pending != nil {
		s.pending.phase = "uncertain"
		s.pending.reasonCode = "runtime_stopped"
		s.emit(protocol.EventCommandRejected, s.pending.command, true)
		s.pending = nil
	}
	if s.aborting != nil {
		d := s.aborting
		d.err = fmt.Errorf("Claude runtime stopped before abort confirmation")
		d.phase = "uncertain"
		d.reasonCode = "runtime_stopped"
		s.emit(protocol.EventCommandRejected, d.command, true)
		s.aborting = nil
	}
	s.cancelQueued("Claude runtime stopped before follow-up delivery")
}
