package claude

import (
	"context"
	"errors"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"time"
)

type inputError struct {
	code string
	err  error
}

func (e inputError) Error() string              { return e.err.Error() }
func (e inputError) Unwrap() error              { return e.err }
func inputFailure(code string, err error) error { return inputError{code: code, err: err} }
func failureCode(err error) string {
	var classified inputError
	if errors.As(err, &classified) {
		return classified.code
	}
	var missing editorNotVisible
	if errors.As(err, &missing) {
		return "editor_not_visible"
	}
	return "delivery_failed"
}
func (s *tuiControls) deliveryFacts(c protocol.ControlCommand) (string, string) {
	d := s.seen[c.ID]
	if d == nil {
		return "not_sent", "delivery_failed"
	}
	switch d.phase {
	case "queued":
		if c.Type == protocol.CommandFollowUp {
			return "queued", "queued_follow_up"
		}
		return "queued", "queued_startup"
	case "submitted":
		return "submitted", "command_submitted"
	case "confirmed":
		return "confirmed", "native_confirmed"
	case "failed":
		return "not_sent", failureCode(d.err)
	case "cancelled":
		return "cancelled", "queued_cancelled"
	case "uncertain":
		if d.reasonCode != "" {
			return "uncertain", d.reasonCode
		}
		return "uncertain", failureCode(d.err)
	}
	return "", ""
}

type readinessTracker struct {
	started, nextProbe time.Time
	state, reason      string
	readyEmitted       bool
}

func (r *readinessTracker) observe(ctx context.Context, s *tuiControls, now time.Time) (string, string) {
	if s.stopping {
		return "stopping", "runtime_stopping"
	}
	if s.aborting != nil && s.aborting.phase == "uncertain" {
		return "blocked", "abort_uncertain"
	}
	if s.pending != nil && s.pending.phase == "uncertain" {
		return "blocked", failureCode(s.pending.err)
	}
	if !s.ready && s.lastFailureCode != "" {
		return "blocked", s.lastFailureCode
	}
	if s.busy && s.asking != "" {
		return "waiting_input", s.asking
	}
	if s.busy {
		if s.executionPending {
			return "awaiting_confirmation", "execution_pending"
		}
		return "busy", "active_turn"
	}
	if s.pending != nil || s.aborting != nil {
		return "awaiting_confirmation", "command_submitted"
	}
	if !s.sessionStarted {
		if now.Sub(r.started) < 3*time.Second {
			return "starting", "runtime_starting"
		}
		return "waiting_input", "session_start_pending"
	}
	if now.Before(s.inputReadyAt) {
		return "starting", "runtime_starting"
	}
	if s.input == nil {
		return "blocked", "controls_unavailable"
	}
	inspector, ok := s.input.(interface {
		verify(context.Context, bool) error
	})
	if !ok {
		return "ready", "editor_ready"
	}
	if err := inspector.verify(ctx, true); err != nil {
		code := failureCode(err)
		switch code {
		case "editor_not_visible", "editor_not_empty":
			return "waiting_input", code
		default:
			return "blocked", code
		}
	}
	return "ready", "editor_ready"
}
func (r *readinessTracker) tick(ctx context.Context, s *tuiControls, now time.Time, emit func(protocol.RuntimeEvent) error) {
	if now.Before(r.nextProbe) {
		return
	}
	r.nextProbe = now.Add(time.Second)
	state, reason := r.observe(ctx, s, now)
	if state != r.state || reason != r.reason {
		r.state, r.reason = state, reason
		_ = emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeStatus, Readiness: state, ReasonCode: reason})
	}
	if !r.readyEmitted && (state == "ready" || state == "busy") {
		r.readyEmitted = true
		_ = emit(protocol.RuntimeEvent{Type: protocol.EventRuntimeReady, Readiness: state, ReasonCode: reason})
	}
}
