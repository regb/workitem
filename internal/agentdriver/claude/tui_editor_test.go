package claude

import (
	"context"
	"errors"
	"strings"
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
	tmux := newFakeTmux("loading")
	captures, afterPaste := 0, 0
	tmux.react = func(args []string) {
		switch args[0] {
		case "capture-pane":
			// The editor appears late, and the pasted text later still.
			if captures++; captures > 1 {
				tmux.screen = "───\n❯\n───"
			}
			if tmux.count("paste-buffer") > 0 {
				if afterPaste++; afterPaste >= 3 {
					tmux.screen = "───\n❯ " + tmux.payload + "\n───"
				}
			}
		case "send-keys":
			if afterPaste < 3 {
				t.Fatal("submitted before readback")
			}
		}
	}
	if err := tmux.input().Send(context.Background(), "private prompt", false); err != nil {
		t.Fatal(err)
	}
	if pastes, keys, deletes := tmux.count("paste-buffer"), tmux.count("send-keys"), tmux.count("delete-buffer"); pastes != 1 || keys != 1 || deletes != 1 {
		t.Fatal(pastes, keys, deletes)
	}
}
func TestMissingOrChangedPasteReadbackNeverSubmits(t *testing.T) {
	for _, post := range []string{"───\n❯\n───", "───\n❯ unrelated draft\n───", "───\n❯ [Pasted text #1 +20 lines]\n───"} {
		tmux := newFakeTmux("───\n❯\n───")
		tmux.react = func(args []string) {
			if args[0] == "paste-buffer" {
				tmux.screen = post
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := tmux.input().Send(ctx, "private prompt", false)
		cancel()
		var before beforeInputError
		if pastes, keys, deletes := tmux.count("paste-buffer"), tmux.count("send-keys"), tmux.count("delete-buffer"); err == nil || errors.As(err, &before) || !strings.Contains(err.Error(), "uncertain") || pastes != 1 || keys != 0 || deletes != 1 {
			t.Fatal(err, pastes, keys, deletes)
		}
	}
}
func TestUnavailableEditorNeverTypesAndOwnershipFailureDoesNotPoll(t *testing.T) {
	for _, screen := range []string{"loading", "───\n❯ existing draft\n───", "───\n❯\n  continuation draft\n───", "───\n❯ No, exit\nYes, trust\n───"} {
		tmux := newFakeTmux(screen)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := tmux.input().Send(ctx, "work", false)
		cancel()
		var before beforeInputError
		if mutations := len(tmux.calls) - tmux.count("display-message") - tmux.count("capture-pane"); !errors.As(err, &before) || mutations != 0 {
			t.Fatal(err, mutations)
		}
	}
	tmux := newFakeTmux("───\n❯\n───")
	tmux.meta = "%8|1|0|0|0"
	if err := tmux.input().Send(context.Background(), "work", false); err == nil || len(tmux.calls) != 1 {
		t.Fatal(err, tmux.calls)
	}
}
