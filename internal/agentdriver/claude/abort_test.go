package claude

import (
	"context"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestAbortInterruptsOnceClearsQueueAndConfirmsSettlement(t *testing.T) {
	s, input, events := testControls()
	ctx := context.Background()
	s.hook("SessionStart", "")
	s.hook("UserPromptSubmit", "")
	follow := control("follow", protocol.CommandFollowUp, "private follow-up")
	if err := s.accept(ctx, follow); err != nil {
		t.Fatal(err)
	}
	abort := control("abort", protocol.CommandAbort, "")
	if err := s.accept(ctx, abort); err != nil {
		t.Fatal(err)
	}
	if len(s.queue) != 0 || s.seen[follow.ID].err == nil || input.interrupts != 1 || s.aborting == nil || !s.busy {
		t.Fatalf("state=%+v input=%+v", s, input)
	}
	if err := s.accept(ctx, abort); err != nil {
		t.Fatal(err)
	}
	if input.interrupts != 1 {
		t.Fatal("abort retry sent another Escape")
	}
	if err := s.accept(ctx, control("other-abort", protocol.CommandAbort, "")); err == nil {
		t.Fatal("allowed concurrent interrupt")
	}
	if err := s.accept(ctx, control("work", protocol.CommandSteer, "work")); err == nil {
		t.Fatal("accepted new work before abort confirmation")
	}
	s.hook("PostToolUseFailure", "")
	if s.aborting == nil {
		t.Fatal("tool failure incorrectly confirmed settled turn")
	}
	s.hook("Stop", "")
	if s.aborting != nil || s.busy {
		t.Fatal("abort not settled")
	}
	last := (*events)[len(*events)-1]
	if last.Type != protocol.EventCommandAccepted || last.CommandID != abort.ID {
		t.Fatal(last)
	}
	s.tick(ctx, time.Now().Add(time.Second))
	if len(input.messages) != 0 {
		t.Fatal("aborted follow-up was delivered")
	}
	if err := s.accept(ctx, follow); err == nil {
		t.Fatal("cancelled follow-up retry was accepted")
	}
}
func TestIdleAndStartupAbortAreLocalNoops(t *testing.T) {
	for _, ready := range []bool{false, true} {
		s, input, _ := testControls()
		if ready {
			s.hook("SessionStart", "")
		}
		if err := s.accept(context.Background(), control("queued", protocol.CommandFollowUp, "later")); err != nil {
			t.Fatal(err)
		}
		if err := s.accept(context.Background(), control("abort", protocol.CommandAbort, "")); err != nil {
			t.Fatal(err)
		}
		if len(s.queue) != 0 || input.interrupts != 0 || s.aborting != nil {
			t.Fatalf("%+v", s)
		}
	}
}
func TestAbortPendingSubmissionAndUncertainTimeout(t *testing.T) {
	s, input, events := testControls()
	s.hook("SessionStart", "")
	s.inputReadyAt = time.Time{}
	ctx := context.Background()
	if err := s.accept(ctx, control("prompt", protocol.CommandPrompt, "work")); err != nil {
		t.Fatal(err)
	}
	input.err = fmt.Errorf("tmux delivery uncertain")
	c := control("abort", protocol.CommandAbort, "")
	if err := s.accept(ctx, c); err == nil {
		t.Fatal("lost interrupt error")
	}
	if s.pending != nil || s.seen["prompt"].err == nil {
		t.Fatal("left pending input after abort")
	}
	s.tick(ctx, time.Now().Add(time.Minute))
	if s.aborting != nil || s.ready {
		t.Fatal("assumed unconfirmed abort settled")
	}
	if err := s.accept(ctx, c); err == nil {
		t.Fatal("retry lost uncertain outcome")
	}
	if input.interrupts != 1 {
		t.Fatal("retried Escape")
	}
	last := (*events)[len(*events)-1]
	if last.CommandID != c.ID || last.Type != protocol.EventCommandRejected {
		t.Fatal(last)
	}
}
func TestAbortValidationAndRuntimeExit(t *testing.T) {
	s, input, _ := testControls()
	ctx := context.Background()
	s.hook("SessionStart", "")
	s.hook("PreToolUse", "")
	c := control("guard", protocol.CommandAbort, "")
	c.RequireIdle = true
	if err := s.accept(ctx, c); err == nil {
		t.Fatal("ignored idle requirement")
	}
	c.RequireIdle = false
	c.Message = "text"
	if err := s.accept(ctx, c); err == nil {
		t.Fatal("accepted abort body")
	}
	c.Message = ""
	c.Images = []protocol.ImageContent{{Data: "private"}}
	if err := s.accept(ctx, c); err == nil {
		t.Fatal("accepted abort image")
	}
	c.Images = nil
	if err := s.accept(ctx, c); err != nil {
		t.Fatal(err)
	}
	s.close()
	if s.seen[c.ID].err == nil || s.aborting != nil || input.interrupts != 1 {
		t.Fatal("lost exit outcome")
	}
}
func TestTmuxInterruptSendsSingleEscapeAndPreservesDrafts(t *testing.T) {
	calls := []string{}
	screen := "history\n───\n❯ \n───\nfooter"
	metadata := fmt.Sprintf("%%7|%d|0|0|0", syscall.Getpgrp())
	input := &tmuxInput{pane: "%7", owner: syscall.Getpgrp(), run: func(_ context.Context, _ io.Reader, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "display-message":
			return metadata, nil
		case "capture-pane":
			return screen, nil
		}
		return "", nil
	}}
	if err := input.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, call := range calls {
		if strings.HasPrefix(call, "send-keys") {
			count++
			if call != "send-keys -t %7 Escape" {
				t.Fatal(call)
			}
		}
	}
	if count != 1 {
		t.Fatal(calls)
	}
	calls = nil
	screen = "───\n❯ existing draft\n───"
	if err := input.Interrupt(context.Background()); err == nil {
		t.Fatal("interrupted draft or modal UI")
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "send-keys") {
			t.Fatal("sent Escape over draft")
		}
	}
	calls = nil
	metadata = "%99|1|0|0|0"
	if err := input.Interrupt(context.Background()); err == nil {
		t.Fatal("interrupted replacement pane")
	}
}
