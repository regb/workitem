package claude

import (
	"context"
	"fmt"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"testing"
	"time"
)

type statusInput struct {
	fakeTUIInput
	inspection error
}

func (s *statusInput) verify(context.Context, bool) error { return s.inspection }
func TestReadinessIsDistinctFromProcessStartupAndHook(t *testing.T) {
	now := time.Now()
	r := &readinessTracker{started: now}
	s, _, _ := testControls()
	input := &statusInput{inspection: editorNotVisible{}}
	s.input = input
	check := func(at time.Time, want, reason string) {
		t.Helper()
		state, code := r.observe(context.Background(), s, at)
		if state != want || code != reason {
			t.Fatal(state, code, want, reason)
		}
	}
	check(now, "starting", "runtime_starting")
	check(now.Add(4*time.Second), "waiting_input", "session_start_pending")
	s.hook("SessionStart", "")
	check(s.inputReadyAt.Add(-time.Millisecond), "starting", "runtime_starting")
	check(s.inputReadyAt.Add(time.Second), "waiting_input", "editor_not_visible")
	input.inspection = nil
	check(s.inputReadyAt.Add(time.Second), "ready", "editor_ready")
	input.inspection = inputFailure("editor_not_empty", fmt.Errorf("private draft must never become event text"))
	check(s.inputReadyAt.Add(time.Second), "waiting_input", "editor_not_empty")
	s.busy = true
	check(now.Add(5*time.Second), "busy", "active_turn")
}
func TestReadinessEventsOnlyChangeAndFailuresStayBlocked(t *testing.T) {
	now := time.Now()
	r := &readinessTracker{started: now}
	s, _, _ := testControls()
	events := []protocol.RuntimeEvent{}
	emit := func(e protocol.RuntimeEvent) error { events = append(events, e); return nil }
	r.tick(context.Background(), s, now, emit)
	r.tick(context.Background(), s, now.Add(time.Second), emit)
	if len(events) != 1 {
		t.Fatal(events)
	}
	s.sessionStarted = true
	s.ready = true
	r.tick(context.Background(), s, now.Add(2*time.Second), emit)
	if len(events) != 3 || events[2].Type != protocol.EventRuntimeReady {
		t.Fatal(events)
	}
	s.ready = false
	s.lastFailureCode = "confirmation_timeout"
	r.tick(context.Background(), s, now.Add(3*time.Second), emit)
	if events[len(events)-1].Readiness != "blocked" {
		t.Fatal(events)
	}
	c := control("failed", protocol.CommandPrompt, "secret")
	s.seen = map[string]*commandDelivery{c.ID: {command: c, phase: "uncertain", reasonCode: "confirmation_timeout", err: fmt.Errorf("private native failure")}}
	state, code := s.deliveryFacts(c)
	if state != "uncertain" || code != "confirmation_timeout" {
		t.Fatal(state, code)
	}
}
func TestAQuestionPausesTheTurnForTheOperator(t *testing.T) {
	now := time.Now()
	r := &readinessTracker{started: now}
	s, _, _ := testControls()
	s.hook("SessionStart", "")
	s.hook("UserPromptSubmit", "")
	check := func(want, reason string) {
		t.Helper()
		if state, code := r.observe(context.Background(), s, now); state != want || code != reason {
			t.Fatal(state, code, want, reason)
		}
	}
	steps := []struct{ event, tool, notification, state, reason string }{
		{"PreToolUse", "AskUserQuestion", "", "waiting_input", "question_pending"},
		{"PostToolUse", "AskUserQuestion", "", "busy", "active_turn"},
		{"PreToolUse", "Bash", "", "busy", "active_turn"},
		{"Notification", "", "permission_prompt", "waiting_input", "permission_pending"},
		{"Notification", "", "idle_prompt", "waiting_input", "permission_pending"},
		{"PostToolUseFailure", "Bash", "", "busy", "active_turn"},
		{"PreToolUse", "ExitPlanMode", "", "waiting_input", "plan_approval_pending"},
	}
	for _, step := range steps {
		s.hook(step.event, "")
		s.observeQuestion(step.event, step.tool, step.notification)
		check(step.state, step.reason)
	}
	s.hook("Stop", "")
	s.observeQuestion("Stop", "", "")
	if s.asking != "" {
		t.Fatal(s.asking)
	}
}
