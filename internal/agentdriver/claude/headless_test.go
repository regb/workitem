package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestHeadlessFrameReader(t *testing.T) {
	for _, tc := range []struct {
		input   string
		frames  int
		failure bool
	}{{"{\"type\":\"system\"}\n", 1, false}, {"{\"type\":\"result\"}", 0, true}, {"bad\n", 0, true}, {"{}\n", 0, true}, {strings.Repeat("x", maxHeadlessFrame) + "\n", 0, true}} {
		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan headlessRead, 2)
		go readHeadlessFrames(ctx, strings.NewReader(tc.input), out)
		count := 0
		for {
			v := <-out
			if v.err != nil {
				if (v.err != io.EOF) != tc.failure {
					t.Fatal(v.err)
				}
				break
			}
			count++
		}
		cancel()
		if count != tc.frames {
			t.Fatal(count)
		}
	}
	for _, v := range []string{"", "2.1.298", "garbage"} {
		if supportedHeadlessVersion(v) {
			t.Fatal(v)
		}
	}
	if !supportedHeadlessVersion("2.1.299") {
		t.Fatal("tested version refused")
	}
}
func TestHeadlessCommandsReserveDeduplicateAndCorrelate(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	received := make(chan map[string]any, 8)
	go func() {
		d := json.NewDecoder(r)
		for {
			var x map[string]any
			if d.Decode(&x) != nil {
				return
			}
			received <- x
		}
	}()
	events := []protocol.RuntimeEvent{}
	s := &headlessControls{input: w, session: "session", initialized: true, cancelQueued: true, emit: func(e protocol.RuntimeEvent) { events = append(events, e) }}
	command := func(id, kind string) protocol.ControlCommand {
		return protocol.ControlCommand{ID: id, Type: kind, Message: "private prompt", ProtocolVersion: protocol.RuntimeVersion}
	}
	a := command("a", protocol.CommandPrompt)
	if err := s.accept(a); err != nil {
		t.Fatal(err)
	}
	first := <-received
	aid := first["uuid"].(string)
	if err := s.accept(a); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
		t.Fatal("duplicate sent")
	default:
	}
	if !s.busy() {
		t.Fatal("submitted work not reserved")
	}
	s.confirm(aid)
	b := command("b", protocol.CommandFollowUp)
	if err := s.accept(b); err != nil {
		t.Fatal(err)
	}
	s.tick(time.Now())
	select {
	case <-received:
		t.Fatal("follow-up sent during active turn")
	default:
	}
	matched, _ := s.result(headlessFrame{Subtype: "success", UserIDs: []string{aid}})
	if !matched {
		t.Fatal("result not correlated")
	}
	s.tick(time.Now())
	second := <-received
	bid := second["uuid"].(string)
	if !s.busy() {
		t.Fatal("next turn not reserved")
	}
	s.result(headlessFrame{Subtype: "success", UserIDs: []string{aid}})
	if !s.busy() {
		t.Fatal("late result cleared new turn")
	}
	c := command("c", protocol.CommandSteer)
	if err := s.accept(c); err != nil {
		t.Fatal(err)
	}
	third := <-received
	if third["priority"] != "next" {
		t.Fatal(third)
	}
	cid := third["uuid"].(string)
	s.result(headlessFrame{Subtype: "success", UserIDs: []string{bid, cid}, IsError: true})
	if s.busy() {
		t.Fatal("merged result left reservation")
	}
	guarded := command("guard", protocol.CommandPrompt)
	guarded.RequireIdle = true
	if s.accept(guarded) == nil {
		t.Fatal("atomic guard falsely supported")
	}
	abort := protocol.ControlCommand{ID: "abort", Type: protocol.CommandAbort}
	if err := s.accept(abort); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
		t.Fatal("idle abort wrote native input")
	default:
	}
	for _, d := range s.known {
		if d.command.Message != "" || len(d.command.Images) > 0 {
			t.Fatal("retained native input")
		}
	}
	for _, event := range events {
		raw, _ := json.Marshal(event)
		if strings.Contains(string(raw), "private prompt") {
			t.Fatal("prompt entered compact event")
		}
	}
}
func TestHeadlessMissingReceiptAndInterruptFence(t *testing.T) {
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	frames := make(chan headlessFrame, 8)
	go func() {
		d := json.NewDecoder(r)
		for {
			var f headlessFrame
			if d.Decode(&f) != nil {
				return
			}
			frames <- f
		}
	}()
	s := &headlessControls{input: w, initialized: true, cancelQueued: true, emit: func(protocol.RuntimeEvent) {}}
	c := protocol.ControlCommand{ID: "missing-receipt", Type: protocol.CommandPrompt, Message: "text"}
	if err := s.accept(c); err != nil {
		t.Fatal(err)
	}
	f := <-frames
	d := s.work[f.UUID]
	follow := protocol.ControlCommand{ID: "follow", Type: protocol.CommandFollowUp, Message: "later"}
	if err := s.accept(follow); err != nil {
		t.Fatal(err)
	}
	s.tick(time.Now().Add(16 * time.Second))
	if d.phase != "uncertain" || !s.busy() {
		t.Fatal("uncertainty lost reservation")
	}
	if s.accept(protocol.ControlCommand{ID: "new-steer", Type: protocol.CommandSteer, Message: "not sent"}) == nil {
		t.Fatal("new input accepted during uncertainty")
	}
	s.tick(time.Now())
	select {
	case <-frames:
		t.Fatal("uncertain delivery retried or queue drained")
	default:
	}
	abort := protocol.ControlCommand{ID: "abort", Type: protocol.CommandAbort}
	if err := s.accept(abort); err != nil {
		t.Fatal(err)
	}
	a := <-frames
	if a.Request.Subtype != "interrupt" || a.RequestID != s.aborting.nativeID {
		t.Fatal(a)
	}
	if len(s.queue) != 0 {
		t.Fatal("own queue not cancelled")
	}
	response := headlessFrame{Type: "control_response"}
	response.Response.RequestID = a.RequestID
	response.Response.Subtype = "success"
	response.Response.Response.Cancelled = []string{f.UUID}
	response.Response.Response.Queued = []string{}
	s.interruptResponse(response)
	if s.busy() {
		t.Fatal("native queue cancellation not released")
	}
	if s.known["follow"].command.Message != "" || s.known["missing-receipt"].command.Message != "" {
		t.Fatal("input retained")
	}
}
func TestHeadlessPartialWritePoisonsNativeTransport(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	s := &headlessControls{input: w, initialized: true, emit: func(protocol.RuntimeEvent) {}}
	c := protocol.ControlCommand{ID: "partial", Type: protocol.CommandPrompt, Message: strings.Repeat("x", 64<<10)}
	start := time.Now()
	if s.accept(c) == nil {
		t.Fatal("unread pipe write completed")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("write not bounded")
	}
	if !s.inputBroken || s.known[c.ID].phase != "uncertain" || !s.busy() {
		t.Fatal("partial delivery not fenced")
	}
	if s.accept(protocol.ControlCommand{ID: "steer", Type: protocol.CommandSteer, Message: "unsafe"}) == nil {
		t.Fatal("write after damaged framing")
	}
	if s.accept(protocol.ControlCommand{ID: "abort", Type: protocol.CommandAbort}) == nil {
		t.Fatal("interrupt after damaged framing")
	}
	s.tick(time.Now().Add(20 * time.Second))
	if !s.busy() {
		t.Fatal("uncertain reservation expired")
	}
}
func TestHeadlessUncertainDeliveryNeverRetries(t *testing.T) {
	r, w, _ := os.Pipe()
	defer w.Close()
	r.Close()
	s := &headlessControls{input: w, initialized: true, emit: func(protocol.RuntimeEvent) {}}
	c := protocol.ControlCommand{ID: "broken", Type: protocol.CommandPrompt, Message: "text"}
	if s.accept(c) == nil {
		t.Fatal("broken pipe succeeded")
	}
	if s.accept(c) == nil {
		t.Fatal("duplicate failure changed")
	}
	if len(s.work) != 0 {
		t.Fatal("zero-byte failure reserved input")
	}
}

func TestHeadlessRuntimeMultiTurnAndShutdown(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		t.Run(fmt.Sprint(ignore), func(t *testing.T) {
			dir := t.TempDir()
			backend := filepath.Join(dir, "backend")
			script := "#!/bin/sh\nWI_CLAUDE_HEADLESS_TEST=1 exec " + quote(os.Args[0]) + " -test.run=TestClaudeHeadlessBackendProcess -- \"$@\"\n"
			if err := os.WriteFile(backend, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			runtimeID := "headless-runtime"
			itemID := "item"
			control := filepath.Join(dir, "control.sock")
			events, err := protocol.ListenEvents(filepath.Join(dir, "events.sock"))
			if err != nil {
				t.Fatal(err)
			}
			defer events.Close()
			var mu sync.Mutex
			captured := []protocol.RuntimeEvent{}
			go func() {
				for r := range events.Requests() {
					mu.Lock()
					captured = append(captured, r.Event)
					mu.Unlock()
					r.Respond(nil)
				}
			}()
			env := map[string]string{"HOME": dir, "CLAUDE_CONFIG_DIR": filepath.Join(dir, ".claude"), "TMUX": "must-not-reach-backend", "WI_CLAUDE_FAKE_IGNORE_TERM": fmt.Sprint(ignore)}
			locator := filepath.Join(dir, "sessions", uuid("headless")+".jsonl")
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			ready := make(chan protocol.Ready, 1)
			done := make(chan error, 1)
			go func() {
				done <- launch(ctx, protocol.LaunchRequest{Runtime: protocol.Runtime{ID: runtimeID, WorkItemID: itemID, Mode: protocol.ModeHeadless, CWD: dir, ControlSocketPath: control, EventSocketPath: eventsPath(dir)}, Conversation: protocol.Conversation{Locator: locator}, Environment: env}, Config{Executable: backend}, func(r protocol.Ready) error { ready <- r; return nil })
			}()
			select {
			case r := <-ready:
				if r.Error != "" || !r.Capabilities.Headless || !r.Capabilities.Streaming || !r.Capabilities.ImageInput || r.Capabilities.NativeTUI || r.Capabilities.IdleGuard {
					t.Fatal(r)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			send := func(id, kind, message string) {
				t.Helper()
				if err := protocol.SubmitControl(ctx, control, protocol.ControlCommand{ProtocolVersion: protocol.RuntimeVersion, RuntimeID: runtimeID, WorkItemID: itemID, ID: id, RequestID: "req-" + id, Type: kind, Message: message}); err != nil {
					t.Fatal(err)
				}
			}
			wait := func(id string) {
				t.Helper()
				for end := time.Now().Add(3 * time.Second); time.Now().Before(end); {
					mu.Lock()
					found := false
					invalidOutcome := false
					for _, e := range captured {
						if e.Type == protocol.EventAgentSettled && e.CommandID == id {
							if (e.Failed && e.Outcome != "failed") || (!e.Failed && e.Outcome != "completed") {
								invalidOutcome = true
							}
							found = true
						}
					}
					mu.Unlock()
					if invalidOutcome {
						t.Fatal("settlement missing explicit outcome", id)
					}
					if found {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("turn not settled", id)
			}
			send("one", protocol.CommandPrompt, "first private prompt")
			wait("one")
			send("two", protocol.CommandPrompt, "second private prompt")
			wait("two")
			send("two", protocol.CommandPrompt, "second private prompt")
			content, err := protocol.ReadContent(ctx, protocol.ContentSocketPath(control), runtimeID, itemID)
			if err != nil || content.Text != "answer 2" {
				t.Fatal(content, err)
			}
			// Each prompt is linked, for its sender, to the record uuid wi chose.
			if links := content.Prompts; len(links) != 2 || links[1].RequestID != "req-two" || links[1].NativeIDs[0] != uuid("headless-command:two") {
				t.Fatalf("prompt links = %+v", links)
			}
			send("stop", protocol.CommandShutdown, "")
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			mu.Lock()
			defer mu.Unlock()
			for _, e := range captured {
				raw, _ := json.Marshal(e)
				if strings.Contains(string(raw), "private prompt") || strings.Contains(string(raw), "answer ") {
					t.Fatal("native text retained")
				}
			}
		})
	}
}
func eventsPath(dir string) string { return filepath.Join(dir, "events.sock") }
func TestClaudeHeadlessBackendProcess(t *testing.T) {
	if os.Getenv("WI_CLAUDE_HEADLESS_TEST") != "1" {
		return
	}
	if os.Getenv("TMUX") != "" {
		os.Exit(31)
	}
	if os.Getenv("WI_CLAUDE_FAKE_IGNORE_TERM") == "true" {
		signal.Ignore(syscall.SIGTERM)
	}
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if enc.Encode(v) != nil {
			os.Exit(32)
		}
	}
	scenario := os.Getenv("WI_CLAUDE_FAKE_SCENARIO")
	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 4096), maxHeadlessFrame)
	count := 0
	for scan.Scan() {
		var x struct {
			Response struct {
				Response struct {
					Behavior string `json:"behavior"`
				} `json:"response"`
			} `json:"response"`
			Type, UUID string
			RequestID  string `json:"request_id"`
			Request    struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if json.Unmarshal(scan.Bytes(), &x) != nil {
			os.Exit(33)
		}
		switch x.Type {
		case "control_request":
			payload := map[string]any{"claude_code_version": "2.1.299", "session_state": "idle"}
			if scenario == "missing-state" {
				delete(payload, "session_state")
			}
			if scenario == "old-version" {
				payload["claude_code_version"] = "2.1.298"
			}
			send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": x.RequestID, "response": payload}})
		case "control_response":
			if x.Response.Response.Behavior != "deny" {
				os.Exit(34)
			}
		case "user":
			caps := []string{"msg_lifecycle_v1", "request_marker_lists_v1", "interrupt_receipt_v1", "interrupt_cancel_queued_v1"}
			if scenario == "missing-capability" {
				caps = caps[:3]
			}
			if scenario != "missing-init" {
				send(map[string]any{"type": "system", "subtype": "init", "capabilities": caps})
			}
			if scenario == "hold" || scenario == "blocked-hook" || scenario == "authentication" || scenario == "api-error" {
				send(map[string]any{"type": "command_lifecycle", "state": "queued", "command_uuid": x.UUID})
				send(map[string]any{"type": "user", "uuid": x.UUID, "isReplay": true})
				if scenario == "hold" {
					continue
				}
				if scenario == "blocked-hook" {
					send(map[string]any{"type": "result", "subtype": "success", "num_turns": 0, "result": "UserPromptSubmit operation blocked by hook:\nprivate hook failure", "user_message_uuids": []string{x.UUID}})
				} else {
					code, category := 500, "server_error"
					if scenario == "authentication" {
						code, category = 401, "authentication_failed"
					}
					send(map[string]any{"type": "assistant", "error": category, "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "private provider failure"}}}, "user_message_uuids": []string{x.UUID}})
					send(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "api_error_status": code, "errors": []string{"private provider failure"}, "user_message_uuids": []string{x.UUID}})
				}
				continue
			}
			send(map[string]any{"type": "control_request", "request_id": "fake-approval", "request": map[string]any{"subtype": "can_use_tool"}})
			count++
			send(map[string]any{"type": "command_lifecycle", "state": "queued", "command_uuid": x.UUID})
			send(map[string]any{"type": "user", "uuid": x.UUID, "isReplay": true})
			send(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start"}})
			send(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": fmt.Sprintf("answer %d", count)}}})
			send(map[string]any{"type": "result", "subtype": "success", "user_message_uuids": []string{x.UUID}})
		}
	}
	os.Exit(0)
}
