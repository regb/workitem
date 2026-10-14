package claude

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const editorWaitTimeout = 3 * time.Second
const editorPollInterval = 50 * time.Millisecond

type editorNotVisible struct{}

func (editorNotVisible) Error() string { return "Claude prompt editor is not visible" }

type beforeInputError struct{ err error }

func (e beforeInputError) Error() string { return e.err.Error() }
func (e beforeInputError) Unwrap() error { return e.err }

// Recognize the bounded chat editor, not an old prompt in scrollback or a
// selected menu row. Continuation rows matter even when its first row is blank.
func editorText(screen string) (string, error) {
	lines := strings.Split(screen, "\n")
	border := func(line string) bool {
		line = strings.TrimSpace(line)
		return len([]rune(line)) >= 3 && strings.Trim(line, "─") == ""
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "❯") {
			continue
		}
		if i == 0 || !border(lines[i-1]) {
			return "", editorNotVisible{}
		}
		body := []string{strings.TrimPrefix(line, "❯")}
		for j := i + 1; j < len(lines); j++ {
			if border(lines[j]) {
				return strings.TrimSpace(strings.Join(body, "\n")), nil
			}
			body = append(body, lines[j])
		}
	}
	return "", editorNotVisible{}
}
func (t *tmuxInput) readEditor(ctx context.Context) (string, error) {
	if err := t.verify(ctx, false); err != nil {
		return "", err
	}
	screen, err := t.run(ctx, nil, "capture-pane", "-p", "-J", "-t", t.pane)
	if err != nil {
		return "", inputFailure("pane_unavailable", err)
	}
	return editorText(screen)
}
func waitEditorPoll(ctx context.Context) error {
	timer := time.NewTimer(editorPollInterval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (t *tmuxInput) waitEmptyEditor(ctx context.Context) error {
	waitCtx, cancel := context.WithTimeout(ctx, editorWaitTimeout)
	defer cancel()
	for {
		text, err := t.readEditor(waitCtx)
		if err == nil {
			if text != "" {
				return inputFailure("editor_not_empty", fmt.Errorf("Claude prompt editor contains a draft or dialog"))
			}
			return nil
		}
		var missing editorNotVisible
		if !errors.As(err, &missing) {
			return err
		}
		if err := waitEditorPoll(waitCtx); err != nil {
			return inputFailure("editor_not_visible", fmt.Errorf("Claude prompt editor did not become visible; no input sent: %w", err))
		}
	}
}

// Pane readback is supporting evidence, not native acceptance. Wrapping and
// editor indentation change whitespace; UserPromptSubmit must still match the
// full prompt digest. A newly rendered collapsed paste marker with the expected
// newline count corroborates receipt, but does not verify the hidden contents.
var collapsedPaste = regexp.MustCompile(`^\[Pasted text #[1-9][0-9]* \+([0-9]+) lines\]$`)

func draftMatches(text, message string) bool {
	compact := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, s)
	}
	if marker := collapsedPaste.FindStringSubmatch(strings.TrimSpace(text)); len(marker) == 2 {
		count, err := strconv.Atoi(marker[1])
		if err == nil && count > 0 && count == strings.Count(message, "\n") {
			return true
		}
	}
	return text != "" && compact(text) == compact(message)
}
func (t *tmuxInput) waitPastedDraft(ctx context.Context, message string) error {
	waitCtx, cancel := context.WithTimeout(ctx, editorWaitTimeout)
	defer cancel()
	for {
		text, err := t.readEditor(waitCtx)
		if err == nil && draftMatches(text, message) {
			return nil
		}
		var missing editorNotVisible
		if err != nil && !errors.As(err, &missing) {
			return err
		}
		if err := waitEditorPoll(waitCtx); err != nil {
			return fmt.Errorf("Claude pasted draft was not confirmed by pane readback: %w", err)
		}
	}
}
