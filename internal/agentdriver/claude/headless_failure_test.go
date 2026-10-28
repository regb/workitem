package claude

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestHeadlessSanitizedNativeFrames(t *testing.T) {
	input, err := os.Open("testdata/headless-2.1.299.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan headlessRead, 1)
	go readHeadlessFrames(ctx, input, frames)
	socket := filepath.Join(t.TempDir(), "content.sock")
	content, err := protocol.ListenContent(socket, "runtime", "item", false)
	if err != nil {
		t.Fatal(err)
	}
	defer content.Close()
	events := []protocol.RuntimeEvent{}
	emit := func(e protocol.RuntimeEvent) { events = append(events, e) }
	s := headlessControls{work: map[string]*headlessDelivery{}, nativeKnown: map[string]*headlessDelivery{}, emit: emit}
	display := headlessContent{server: content, emit: emit}
	settled, initialized, interrupts := 0, 0, 0
	for {
		item := <-frames
		if item.err != nil {
			if item.err != io.EOF {
				t.Fatal(item.err)
			}
			break
		}
		f := item.frame
		switch f.Type {
		case "control_response":
			if f.Response.RequestID == "wi-headless-initialize" {
				if !supportedHeadlessVersion(f.Response.Response.Version) || f.Response.Response.State != "idle" {
					t.Fatal("initialize shape changed")
				}
			} else {
				if f.Response.Response.Queued == nil || f.Response.Response.Cancelled == nil {
					t.Fatal("interrupt receipt shape changed")
				}
				interrupts++
			}
		case "system":
			if f.Subtype == "init" {
				if !supportedHeadlessCapabilities(f.Capabilities) {
					t.Fatal("required native capabilities missing")
				}
				initialized++
			}
		case "command_lifecycle":
			if f.State == "queued" {
				d := &headlessDelivery{nativeID: f.CommandUUID, phase: "submitted", command: protocol.ControlCommand{ID: f.CommandUUID}}
				s.work[f.CommandUUID], s.nativeKnown[f.CommandUUID] = d, d
				s.confirm(f.CommandUUID)
				if d.phase != "confirmed" {
					t.Fatal("lifecycle did not confirm input")
				}
			}
		case "user":
			if f.IsReplay {
				s.confirm(f.UUID)
			}
		case "stream_event", "assistant":
			if err := display.frame(f); err != nil {
				t.Fatal(err)
			}
		case "result":
			matched, failed := s.result(f)
			if !matched || failed || s.busy() {
				t.Fatal("native result did not settle its command")
			}
			settled++
		}
	}
	if settled != 2 || initialized != 2 || interrupts != 1 {
		t.Fatal(settled, initialized, interrupts)
	}
	answer, err := protocol.ReadContent(ctx, socket, "runtime", "item")
	if err != nil || answer.Text != "Fixture answer." {
		t.Fatal(answer.Text, err)
	}
	for _, event := range events {
		data, _ := json.Marshal(event)
		if strings.Contains(string(data), "Fixture answer") {
			t.Fatal("native answer entered compact events")
		}
	}
}

func TestHeadlessRequiredCapabilities(t *testing.T) {
	caps := []string{"msg_lifecycle_v1", "request_marker_lists_v1", "interrupt_receipt_v1", "interrupt_cancel_queued_v1"}
	if !supportedHeadlessCapabilities(append(caps, "future_capability")) {
		t.Fatal("additive capability rejected")
	}
	for i := range caps {
		missing := append(append([]string{}, caps[:i]...), caps[i+1:]...)
		if supportedHeadlessCapabilities(missing) {
			t.Fatal("accepted missing", caps[i])
		}
	}
	if supportedHeadlessCapabilities(nil) {
		t.Fatal("absent capability list accepted")
	}
}

func TestHeadlessFailureResultsAndBackgroundContinuation(t *testing.T) {
	for _, tc := range []struct {
		name, terminal, reason, result string
		status, turns                  int
		failed                         bool
	}{
		{name: "expired authentication", status: 401, reason: "native_auth_failed", failed: true},
		{name: "forbidden account", status: 403, reason: "native_auth_failed", failed: true},
		{name: "API failure", status: 500, terminal: "api_error", reason: "native_api_failed", failed: true},
		{name: "stream interruption", terminal: "aborted_streaming"},
		{name: "tool interruption", terminal: "aborted_tools"},
		{name: "interrupted", terminal: "interrupted"},
		{name: "blocked hook", result: "UserPromptSubmit operation blocked by hook:\nprivate failure detail", reason: "prompt_blocked", failed: true},
		{name: "unknown terminal", terminal: "future_private_error", failed: true},
		{name: "background requested", terminal: "background_requested"},
		{name: "completed", terminal: "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := []protocol.RuntimeEvent{}
			d := &headlessDelivery{nativeID: "native", phase: "confirmed", command: protocol.ControlCommand{ID: "command", RequestID: "request"}}
			s := headlessControls{work: map[string]*headlessDelivery{"native": d}, nativeKnown: map[string]*headlessDelivery{"native": d}, emit: func(e protocol.RuntimeEvent) { events = append(events, e) }}
			matched, failed := s.result(headlessFrame{Subtype: "success", UserIDs: []string{"native"}, TerminalReason: tc.terminal, APIErrorStatus: tc.status, Result: tc.result, NumTurns: tc.turns})
			if !matched || failed != tc.failed || len(events) != 1 || events[0].ReasonCode != tc.reason || events[0].CommandID != "command" || s.busy() {
				t.Fatal(matched, failed, events)
			}
			data, _ := json.Marshal(events)
			if strings.Contains(string(data), "private") || strings.Contains(string(data), "future_private_error") {
				t.Fatal("unrestricted failure retained")
			}
			// A fresh background turn has no Wi input ID. It can become active
			// and settle, but its result cannot release a newer Wi reservation.
			s.nativeBusy = true
			s.result(headlessFrame{Subtype: "success"})
			if s.busy() || events[len(events)-1].CommandID != "" {
				t.Fatal("background result inherited a command identity")
			}
			next := &headlessDelivery{nativeID: "next", phase: "submitted"}
			s.work["next"] = next
			s.nativeBusy = true
			matched, _ = s.result(headlessFrame{Subtype: "success"})
			if matched || s.work["next"] != next || !s.busy() {
				t.Fatal("background result released new input")
			}
			if !s.retiredFrame(headlessFrame{UserIDs: []string{"native"}}) {
				t.Fatal("retired native result not fenced")
			}
		})
	}
}

func TestHeadlessIncompleteInterruptReceiptRetainsFence(t *testing.T) {
	for _, payload := range []string{`{}`, `{"still_queued":null,"cancelled":[]}`, `{"still_queued":[]}`, `{"still_queued":[],"cancelled":null}`, `{"still_queued":["pending"],"cancelled":[]}`} {
		t.Run(payload, func(t *testing.T) {
			events := []protocol.RuntimeEvent{}
			d := &headlessDelivery{nativeID: "interrupt", phase: "submitted", command: protocol.ControlCommand{ID: "abort"}, deadline: time.Now().Add(time.Second)}
			s := headlessControls{initialized: true, cancelQueued: true, aborting: d, emit: func(e protocol.RuntimeEvent) { events = append(events, e) }}
			var f headlessFrame
			if err := json.Unmarshal([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"interrupt","response":`+payload+`}}`), &f); err != nil {
				t.Fatal(err)
			}
			s.interruptResponse(f)
			if s.aborting != d || d.phase != "uncertain" || !d.deadline.IsZero() || len(events) != 1 || events[0].Type != protocol.EventCommandRejected || events[0].DeliveryState != "uncertain" {
				t.Fatal("incomplete receipt claimed confirmation", events)
			}
			if s.accept(protocol.ControlCommand{ID: "new", Type: protocol.CommandPrompt, Message: "not sent"}) == nil {
				t.Fatal("new input sent across interruption fence")
			}
			s.tick(time.Now().Add(time.Hour))
			if s.aborting != d {
				t.Fatal("uncertain interrupt expired")
			}
			s.result(headlessFrame{Subtype: "success"})
			if s.aborting != nil {
				t.Fatal("fresh settlement did not release fence")
			}
			for _, event := range events {
				if event.Type == protocol.EventCommandAccepted && event.CommandID == "abort" {
					t.Fatal("settlement fabricated missing interrupt receipt")
				}
			}
		})
	}
}
