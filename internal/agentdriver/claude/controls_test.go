package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/regb/workitem/internal/testutil"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

type fakeTUIInput struct {
	messages   []string
	steers     []bool
	err        error
	interrupts int
}

func (f *fakeTUIInput) Send(_ context.Context, m string, steer bool) error {
	f.messages = append(f.messages, m)
	f.steers = append(f.steers, steer)
	return f.err
}
func (f *fakeTUIInput) Interrupt(context.Context) error { f.interrupts++; return f.err }

func testControls() (*tuiControls, *fakeTUIInput, *[]protocol.RuntimeEvent) {
	input := &fakeTUIInput{}
	events := &[]protocol.RuntimeEvent{}
	s := &tuiControls{input: input, emit: func(kind string, c protocol.ControlCommand, failed bool) {
		*events = append(*events, protocol.RuntimeEvent{Type: kind, CommandID: c.ID, RequestID: c.RequestID, Failed: failed})
	}}
	return s, input, events
}
func control(id, kind, message string) protocol.ControlCommand {
	return protocol.ControlCommand{ID: id, Type: kind, Message: message, RequestID: "request-" + id}
}
func TestControlsReadinessConfirmationAndDeduplication(t *testing.T) {
	ctx := context.Background()
	s, input, events := testControls()
	c := control("startup", protocol.CommandPrompt, "private\nmultiline")
	if err := s.accept(ctx, c); err != nil {
		t.Fatal(err)
	}
	s.tick(ctx, time.Now().Add(time.Second))
	if len(input.messages) != 0 {
		t.Fatal("typed before readiness")
	}
	s.hook("SessionStart", "")
	s.tick(ctx, time.Now().Add(time.Second))
	if len(input.messages) != 1 || input.messages[0] != c.Message {
		t.Fatal(input.messages)
	}
	s.hook("UserPromptSubmit", promptDigest("unrelated native prompt"))
	if s.pending == nil {
		t.Fatal("confirmed unrelated prompt")
	}
	s.hook("UserPromptSubmit", promptDigest(c.Message))
	if s.pending != nil {
		t.Fatal("not confirmed")
	}
	last := (*events)[len(*events)-1]
	if last.Type != protocol.EventCommandAccepted || last.CommandID != c.ID || last.RequestID != c.RequestID {
		t.Fatal(last)
	}
	if err := s.accept(ctx, c); err != nil {
		t.Fatal(err)
	}
	if len(input.messages) != 1 {
		t.Fatal("retry retyped a prompt")
	}
	c.Message = "different"
	if err := s.accept(ctx, c); err == nil {
		t.Fatal("accepted reused identity")
	}
}
func TestControlsFollowUpWaitsForSettledAndSteeringIsImmediate(t *testing.T) {
	ctx := context.Background()
	s, input, _ := testControls()
	s.hook("SessionStart", "")
	s.hook("UserPromptSubmit", "")
	if err := s.accept(ctx, control("plain", protocol.CommandPrompt, "work")); err == nil {
		t.Fatal("prompt accepted while busy")
	}
	follow := control("follow", protocol.CommandFollowUp, "follow work")
	if err := s.accept(ctx, follow); err != nil {
		t.Fatal(err)
	}
	s.tick(ctx, time.Now().Add(time.Second))
	if len(input.messages) != 0 {
		t.Fatal("follow-up disturbed current turn")
	}
	steer := control("steer", protocol.CommandSteer, "new instruction")
	if err := s.accept(ctx, steer); err != nil {
		t.Fatal(err)
	}
	if len(input.messages) != 1 || !input.steers[0] {
		t.Fatal(input)
	}
	s.hook("UserPromptSubmit", promptDigest(steer.Message))
	s.hook("Stop", "")
	s.tick(ctx, time.Now())
	if len(input.messages) != 1 {
		t.Fatal("typed inside Stop hook")
	}
	s.tick(ctx, time.Now().Add(time.Second))
	if len(input.messages) != 2 || input.steers[1] {
		t.Fatal(input)
	}
	s.hook("UserPromptSubmit", promptDigest(follow.Message))
	if s.pending != nil {
		t.Fatal("follow-up confirmation missing")
	}
}
func TestControlsUncertainDeliveryNeverRetriesAndTimeoutStopsQueue(t *testing.T) {
	ctx := context.Background()
	s, input, events := testControls()
	s.hook("SessionStart", "")
	s.inputReadyAt = time.Time{}
	input.err = fmt.Errorf("delivery uncertain")
	c := control("uncertain", protocol.CommandPrompt, "private work")
	if err := s.accept(ctx, c); err == nil {
		t.Fatal("lost delivery error")
	}
	if err := s.accept(ctx, c); err == nil {
		t.Fatal("retry lost uncertainty")
	}
	if len(input.messages) != 1 {
		t.Fatal("retyped uncertain input")
	}
	follow := control("later", protocol.CommandFollowUp, "later work")
	if err := s.accept(ctx, follow); err != nil {
		t.Fatal(err)
	}
	s.tick(ctx, time.Now().Add(time.Minute))
	s.tick(ctx, time.Now().Add(2*time.Minute))
	if len(input.messages) != 1 || s.ready {
		t.Fatal("drained queue without idle evidence")
	}
	if (*events)[len(*events)-1].Type != protocol.EventCommandRejected {
		t.Fatal(*events)
	}
	s.close()
	if s.seen["later"].err == nil || len(s.queue) != 0 {
		t.Fatal("lost queued shutdown outcome")
	}
}
func TestControlsRejectUnsafeInputAndIdleGuards(t *testing.T) {
	s, input, _ := testControls()
	ctx := context.Background()
	for _, message := range []string{"", "\x1b[1m", "bad\rtext", "bad\x00text", string([]byte{255}), strings.Repeat("x", (64<<10)+1)} {
		if err := s.accept(ctx, control("bad", protocol.CommandPrompt, message)); err == nil {
			t.Fatal("accepted unsafe input")
		}
	}
	c := control("guard", protocol.CommandPrompt, "work")
	c.RequireIdle = true
	if err := s.accept(ctx, c); err == nil {
		t.Fatal("assumed startup idle")
	}
	c.RequireIdle = false
	c.Images = []protocol.ImageContent{{MIMEType: "image/png", Data: "private"}}
	if err := s.accept(ctx, c); err == nil {
		t.Fatal("accepted images")
	}
	if len(input.messages) != 0 {
		t.Fatal(input.messages)
	}
	s.input = nil
	if err := s.accept(ctx, control("missing", protocol.CommandPrompt, "work")); err == nil {
		t.Fatal("accepted without owned tmux")
	}
}
func TestControlsQueueAndDedupBounds(t *testing.T) {
	s, _, _ := testControls()
	ctx := context.Background()
	for i := 0; i < 32; i++ {
		if err := s.accept(ctx, control(fmt.Sprint(i), protocol.CommandFollowUp, "work")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.accept(ctx, control("overflow", protocol.CommandFollowUp, "work")); err == nil {
		t.Fatal("unbounded queue")
	}
	s.queue = nil
	for i := len(s.seen); i < 1024; i++ {
		s.seen[fmt.Sprint(i)] = &commandDelivery{}
	}
	if err := s.accept(ctx, control("capacity", protocol.CommandPrompt, "work")); err == nil {
		t.Fatal("unbounded deduplication map")
	}
}
func TestHookConfirmsDigestWithoutForwardingPrompt(t *testing.T) {
	path := filepath.Join(testutil.ShortTempDir(t), "hook.sock")
	server, err := protocol.ListenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunHook(context.Background(), path, strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"session","prompt":"private prompt body","tool_input":{"secret":"hidden"}}`))
	}()
	request := <-server.Requests()
	if strings.Contains(request.Command.Message, "private") || strings.Contains(request.Command.Message, "hidden") {
		t.Fatal("hook leaked native content")
	}
	var h hookInput
	if err := json.Unmarshal([]byte(request.Command.Message), &h); err != nil {
		t.Fatal(err)
	}
	if h.PromptDigest != promptDigest("private prompt body") {
		t.Fatal(h)
	}
	request.Respond(nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestInvalidCustomKeybindingsDisableCapabilities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	if err := os.WriteFile(path, []byte(`{invalid`), 0600); err != nil {
		t.Fatal(err)
	}
	keys, fingerprint, err := readInputBindings(path)
	input := &tmuxInput{bindingPath: path, bindings: keys, fingerprint: fingerprint, bindingError: err}
	for _, kind := range []string{protocol.CommandPrompt, protocol.CommandSteer, protocol.CommandFollowUp, protocol.CommandAbort} {
		if supportsControl(input, kind) {
			t.Fatal("advertised malformed bindings")
		}
	}
}

func TestTmuxInputBracketedPasteOwnershipAndSteerKeys(t *testing.T) {
	calls := []string{}
	payload := ""
	meta := "%3|" + fmt.Sprint(syscall.Getpgrp()) + "|0|0|0"
	screen := "history\n───\n❯\u00a0\n───\nfooter"
	transport := &tmuxInput{pane: "%3", owner: syscall.Getpgrp(), run: func(_ context.Context, input io.Reader, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if input != nil {
			data, _ := io.ReadAll(input)
			payload = string(data)
		}
		if args[0] == "display-message" {
			return meta, nil
		}
		if args[0] == "capture-pane" {
			return screen, nil
		}
		if args[0] == "paste-buffer" {
			screen = "───\n❯ " + strings.ReplaceAll(payload, "\n", "\n  ") + "\n───"
		}
		if args[0] == "send-keys" {
			screen = "───\n❯\n───"
		}
		return "", nil
	}}
	if err := transport.Send(context.Background(), "private\nmultiline", true); err != nil {
		t.Fatal(err)
	}
	if payload != "private\nmultiline" {
		t.Fatal(payload)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "paste-buffer -p") || !strings.Contains(joined, "send-keys -t %3 C-x C-s") || !strings.Contains(joined, "delete-buffer") || strings.Contains(joined, "private") {
		t.Fatal(joined)
	}
	for _, wrong := range []string{"%4|1|0|0|0", "%3|1|0|0|0", "%3|" + fmt.Sprint(syscall.Getpgrp()) + "|0|1|0"} {
		meta = wrong
		if err := transport.Send(context.Background(), "work", false); err == nil {
			t.Fatal("typed into wrong/mode pane")
		}
	}
	meta = "%3|" + fmt.Sprint(syscall.Getpgrp()) + "|0|0|0"
	screen = "───\n❯ existing draft\n───"
	if err := transport.Send(context.Background(), "work", false); err == nil {
		t.Fatal("typed over draft")
	}
}

// Claude can open a dialog at startup. A queued prompt waits for the screen
// to be free instead of failing, and gives way when the operator types first.
func TestQueuedPromptWaitsForADialogToClose(t *testing.T) {
	ctx := context.Background()
	dialog := beforeInputError{err: inputFailure("editor_not_empty", fmt.Errorf("a dialog is open"))}
	for _, operatorFirst := range []bool{false, true} {
		s, input, events := testControls()
		c := control("startup", protocol.CommandPrompt, "Read this work item")
		if err := s.accept(ctx, c); err != nil {
			t.Fatal(err)
		}
		s.hook("SessionStart", "")
		input.err = dialog
		s.tick(ctx, time.Now().Add(time.Second))
		if len(input.messages) != 1 || len(s.queue) != 1 || s.pending != nil || !s.ready {
			t.Fatalf("dialog failed the prompt: messages=%d queue=%d pending=%v ready=%v", len(input.messages), len(s.queue), s.pending, s.ready)
		}
		for _, event := range *events {
			if event.Type == protocol.EventCommandRejected {
				t.Fatalf("rejected while waiting: %+v", *events)
			}
		}
		if state, reason := s.deliveryFacts(c); state != "queued" || reason != "queued_screen_busy" {
			t.Fatalf("delivery %s %s", state, reason)
		}
		if operatorFirst {
			s.hook("UserPromptSubmit", promptDigest("the operator's own prompt"))
			if len(s.queue) != 0 || (*events)[len(*events)-1].Type != protocol.EventCommandRejected {
				t.Fatalf("waiting prompt followed the operator's: queue=%d events=%+v", len(s.queue), *events)
			}
			continue
		}
		input.err = nil
		s.tick(ctx, time.Now().Add(3*time.Second))
		if len(input.messages) != 2 || s.pending == nil {
			t.Fatalf("not retried once free: messages=%d pending=%v", len(input.messages), s.pending)
		}
	}
}
