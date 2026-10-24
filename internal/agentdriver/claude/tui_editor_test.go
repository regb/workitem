package claude

import (
	"context"
	"errors"
	"fmt"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBoundedEditorParsing(t *testing.T) {
	for _, screen := range []string{"old history\n❯\nfooter", "───\n❯ No, exit\nYes, trust\n", "───\n❯\n", "loading"} {
		if _, err := editorText(screen); err == nil {
			t.Fatal("recognized unbounded prompt", screen)
		}
	}
	text, err := editorText("❯ historical prompt\n───\n❯\u00a0\n  continuation draft\n───\nfooter")
	if err != nil || text != "continuation draft" {
		t.Fatal(text, err)
	}
	// Claude 2.1.310 shows a dim hint in the empty editor; a draft is not dim.
	hint := "───\n\x1b[39m❯ \x1b[2mTry \"edit headless.go to...\"\x1b[0m\n───\n"
	if text, err := editorText(hint); err != nil || text != "" {
		t.Fatalf("dim hint read as a draft: %q %v", text, err)
	}
	draft := "───\n❯ \x1b[1mfix\x1b[22m the \x1b]8;;https://x.test\x1b\\link\x1b]8;;\x1b\\\x1b[2;37m suggestion\x1b[m\n───\n"
	if text, err := editorText(draft); err != nil || text != "fix the link" {
		t.Fatalf("styled draft read as %q %v", text, err)
	}
	colored := "───\n❯ \x1b[38;2;255;2;2mred\x1b[38;5;2m green\x1b[39m\n───\n"
	if text, err := editorText(colored); err != nil || text != "red green" {
		t.Fatalf("truecolor draft read as %q %v", text, err)
	}
	if !draftMatches("work\n  continued", "work continued") || draftMatches("work changed", "work") || !draftMatches("[Pasted text #2 +2 lines]", "first\nsecond\nthird") || draftMatches("[Pasted text #2 +3 lines]", "first\nsecond\nthird") {
		t.Fatal("bad readback matching")
	}
}
func TestDelayedEditorAndPasteReadbackBeforeSingleSubmit(t *testing.T) {
	captures, pastes, keys, deletes := 0, 0, 0, 0
	payload := ""
	afterPaste := 0
	input := &tmuxInput{pane: "%7", owner: syscall.Getpgrp(), run: func(_ context.Context, reader io.Reader, args ...string) (string, error) {
		if reader != nil {
			data, _ := io.ReadAll(reader)
			payload = string(data)
		}
		switch args[0] {
		case "display-message":
			return fmt.Sprintf("%%7|%d|0|0|0", syscall.Getpgrp()), nil
		case "capture-pane":
			captures++
			if captures == 1 {
				return "loading", nil
			}
			if pastes == 0 {
				return "───\n❯\n───", nil
			}
			afterPaste++
			if afterPaste < 3 {
				return "───\n❯\n───", nil
			}
			return "───\n❯ " + payload + "\n───", nil
		case "paste-buffer":
			pastes++
		case "send-keys":
			if afterPaste < 3 {
				t.Fatal("submitted before readback")
			}
			keys++
		case "delete-buffer":
			deletes++
		}
		return "", nil
	}}
	if err := input.Send(context.Background(), "private prompt", false); err != nil {
		t.Fatal(err)
	}
	if pastes != 1 || keys != 1 || deletes != 1 {
		t.Fatal(pastes, keys, deletes)
	}
}
func TestMissingOrChangedPasteReadbackNeverSubmits(t *testing.T) {
	for _, post := range []string{"───\n❯\n───", "───\n❯ unrelated draft\n───", "───\n❯ [Pasted text #1 +20 lines]\n───"} {
		pastes, keys, deletes := 0, 0, 0
		input := &tmuxInput{pane: "%7", owner: syscall.Getpgrp(), run: func(_ context.Context, _ io.Reader, args ...string) (string, error) {
			switch args[0] {
			case "display-message":
				return fmt.Sprintf("%%7|%d|0|0|0", syscall.Getpgrp()), nil
			case "capture-pane":
				if pastes == 0 {
					return "───\n❯\n───", nil
				}
				return post, nil
			case "paste-buffer":
				pastes++
			case "send-keys":
				keys++
			case "delete-buffer":
				deletes++
			}
			return "", nil
		}}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := input.Send(ctx, "private prompt", false)
		cancel()
		var before beforeInputError
		if err == nil || errors.As(err, &before) || !strings.Contains(err.Error(), "uncertain") || pastes != 1 || keys != 0 || deletes != 1 {
			t.Fatal(err, pastes, keys, deletes)
		}
	}
}
func TestUnavailableEditorNeverTypesAndOwnershipFailureDoesNotPoll(t *testing.T) {
	for _, screen := range []string{"loading", "───\n❯ existing draft\n───", "───\n❯\n  continuation draft\n───", "───\n❯ No, exit\nYes, trust\n───"} {
		mutations := 0
		input := &tmuxInput{pane: "%7", owner: syscall.Getpgrp(), run: func(_ context.Context, _ io.Reader, args ...string) (string, error) {
			switch args[0] {
			case "display-message":
				return fmt.Sprintf("%%7|%d|0|0|0", syscall.Getpgrp()), nil
			case "capture-pane":
				return screen, nil
			default:
				mutations++
			}
			return "", nil
		}}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := input.Send(ctx, "work", false)
		cancel()
		var before beforeInputError
		if !errors.As(err, &before) || mutations != 0 {
			t.Fatal(err, mutations)
		}
	}
	calls := 0
	input := &tmuxInput{pane: "%7", owner: syscall.Getpgrp(), run: func(context.Context, io.Reader, ...string) (string, error) { calls++; return "%8|1|0|0|0", nil }}
	if err := input.Send(context.Background(), "work", false); err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
}
func TestStartupGraceQueuesCommandsArrivingAfterSessionHook(t *testing.T) {
	s, input, _ := testControls()
	s.hook("SessionStart", "")
	c := control("early", protocol.CommandPrompt, "work")
	if err := s.accept(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background(), s.inputReadyAt.Add(-time.Millisecond))
	if len(input.messages) != 0 || len(s.queue) != 1 {
		t.Fatal("typed within grace")
	}
	s.tick(context.Background(), s.inputReadyAt.Add(time.Millisecond))
	if len(input.messages) != 1 {
		t.Fatal("did not drain ready queue")
	}
}
func TestStartupBusyHookPreventsQueueDraining(t *testing.T) {
	s, input, _ := testControls()
	s.hook("SessionStart", "")
	if err := s.accept(context.Background(), control("early", protocol.CommandPrompt, "work")); err != nil {
		t.Fatal(err)
	}
	s.hook("UserPromptSubmit", "")
	s.tick(context.Background(), time.Now().Add(time.Second))
	if len(input.messages) != 0 {
		t.Fatal("ignored intervening busy hook")
	}
}
func TestDefinitePreInputFailureRejectsOnlyOnceAndNeverRetries(t *testing.T) {
	s, input, events := testControls()
	s.ready = true
	input.err = beforeInputError{fmt.Errorf("editor unavailable")}
	c := control("early", protocol.CommandPrompt, "work")
	if err := s.accept(context.Background(), c); err == nil {
		t.Fatal("lost error")
	}
	if s.pending != nil || s.seen[c.ID].phase != "failed" {
		t.Fatal("reserved uncertain submission slot")
	}
	if err := s.accept(context.Background(), c); err == nil {
		t.Fatal("dedup lost error")
	}
	s.tick(context.Background(), time.Now().Add(time.Minute))
	if len(input.messages) != 1 || len(*events) != 1 {
		t.Fatal("retried or rejected twice")
	}
}
