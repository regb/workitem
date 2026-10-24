package claude

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

type tuiInput interface {
	Send(context.Context, string, bool) error
	Interrupt(context.Context) error
}

type tmuxInput struct {
	socket, pane string
	owner        int
	bindingPath  string
	bindings     inputBindings
	fingerprint  [32]byte
	bindingError error
	run          func(context.Context, io.Reader, ...string) (string, error)
}

var panePattern = regexp.MustCompile(`^%[0-9]+$`)

func discoverTUIInput(environment []string) tuiInput {
	bindingPath := ""
	for _, entry := range environment {
		if strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
			bindingPath = filepath.Join(strings.TrimPrefix(entry, "CLAUDE_CONFIG_DIR="), "keybindings.json")
		}
	}
	socket := strings.Split(os.Getenv("TMUX"), ",")[0]
	pane := os.Getenv("TMUX_PANE")
	if !filepath.IsAbs(socket) || !panePattern.MatchString(pane) {
		return nil
	}
	executable, err := exec.LookPath("tmux")
	if err != nil {
		return nil
	}
	t := &tmuxInput{socket: socket, pane: pane, owner: syscall.Getpgrp(), bindingPath: bindingPath}
	if bindingPath != "" {
		t.bindings, t.fingerprint, t.bindingError = readInputBindings(bindingPath)
	}
	t.run = func(ctx context.Context, input io.Reader, args ...string) (string, error) {
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(callCtx, executable, append([]string{"-S", socket}, args...)...)
		cmd.Stdin = input
		data, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("tmux operation failed: %w", err)
		}
		return string(data), nil
	}
	return t
}

func supportsControl(input tuiInput, kind string) bool {
	if input == nil {
		return false
	}
	if resolved, ok := input.(interface{ Supports(string) bool }); ok {
		return resolved.Supports(kind)
	}
	return true
}
func (t *tmuxInput) keyPlan() inputBindings {
	if t.bindingPath == "" {
		return defaultInputBindings()
	}
	return t.bindings
}
func (t *tmuxInput) Supports(kind string) bool {
	if t.bindingError != nil {
		return false
	}
	keys := t.keyPlan()
	switch kind {
	case protocol.CommandPrompt, protocol.CommandFollowUp:
		return len(keys.submit) > 0
	case protocol.CommandSteer:
		return len(keys.submit) > 0 && len(keys.sendNow) > 0
	case protocol.CommandAbort:
		return len(keys.interrupt) > 0
	}
	return false
}
func (t *tmuxInput) checkBindings() error {
	if t.bindingError != nil {
		return inputFailure("bindings_unavailable", fmt.Errorf("Claude keybindings cannot be resolved safely"))
	}
	if t.bindingPath != "" {
		_, fingerprint, err := readInputBindings(t.bindingPath)
		if err != nil || fingerprint != t.fingerprint {
			return inputFailure("bindings_changed", fmt.Errorf("Claude keybindings changed or became unreadable; restart the runtime before terminal controls"))
		}
	}
	return nil
}

func (t *tmuxInput) verify(ctx context.Context, editor bool) error {
	if err := t.checkBindings(); err != nil {
		return err
	}
	out, err := t.run(ctx, nil, "display-message", "-p", "-t", t.pane, "#{pane_id}|#{pane_pid}|#{pane_dead}|#{pane_in_mode}|#{pane_input_off}")
	if err != nil {
		return inputFailure("pane_unavailable", err)
	}
	fields := strings.Split(strings.TrimSpace(out), "|")
	if len(fields) != 5 || fields[0] != t.pane || fields[1] != strconv.Itoa(t.owner) || fields[2] != "0" || fields[3] != "0" || fields[4] != "0" {
		return inputFailure("pane_changed", fmt.Errorf("Claude pane ownership or input state changed"))
	}
	group, err := syscall.Getpgid(t.owner)
	if err != nil || group != t.owner {
		return inputFailure("pane_changed", fmt.Errorf("Claude pane process group changed"))
	}
	if editor {
		screen, err := t.run(ctx, nil, "capture-pane", "-p", "-e", "-t", t.pane)
		if err != nil {
			return err
		}
		text, err := editorText(screen)
		if err != nil {
			return err
		}
		if text != "" {
			return inputFailure("editor_not_empty", fmt.Errorf("Claude prompt editor contains a draft or dialog"))
		}
	}
	return nil
}

// Interrupt sends the resolved cancel shortcut once. Repeating it at idle
// could invoke another UI action, so uncertain delivery is never retried.
func (t *tmuxInput) Interrupt(ctx context.Context) error {
	keys := t.keyPlan().interrupt
	if len(keys) == 0 {
		return fmt.Errorf("Claude interrupt binding is unavailable")
	}
	if err := t.verify(ctx, true); err != nil {
		return err
	}
	if _, err := t.run(ctx, nil, append([]string{"send-keys", "-t", t.pane}, keys...)...); err != nil {
		return inputFailure("abort_uncertain", fmt.Errorf("Claude abort delivery uncertain; do not resend: %w", err))
	}
	return nil
}

// Send uses a private bracketed-paste buffer so multiline text is not submitted
// as individual Enter keystrokes. Any failure after paste is an uncertain
// delivery: callers must not retry it automatically.
func (t *tmuxInput) Send(ctx context.Context, message string, steer bool) error {
	return t.sendDraft(ctx, message, steer, "")
}
func (t *tmuxInput) sendDraft(ctx context.Context, message string, steer bool, prefix string) error {
	preFailure := func(err error) error {
		if prefix == "" {
			return beforeInputError{err}
		}
		return fmt.Errorf("Claude delivery uncertain after image attachment; do not resend: %w", err)
	}
	keys := t.keyPlan().submit
	if steer {
		keys = t.keyPlan().sendNow
	}
	if len(keys) == 0 {
		return preFailure(fmt.Errorf("Claude submission binding is unavailable"))
	}
	if prefix == "" {
		if err := t.waitEmptyEditor(ctx); err != nil {
			return preFailure(err)
		}
	} else {
		if err := t.waitPastedDraft(ctx, prefix); err != nil {
			return preFailure(err)
		}
	}
	if message != "" {
		buffer := fmt.Sprintf("wi-claude-%d-%d", os.Getpid(), time.Now().UnixNano())
		if _, err := t.run(ctx, strings.NewReader(message), "load-buffer", "-b", buffer, "-"); err != nil {
			return preFailure(err)
		}
		defer func() { _, _ = t.run(context.Background(), nil, "delete-buffer", "-b", buffer) }()
		if prefix == "" {
			if err := t.verify(ctx, true); err != nil {
				return preFailure(err)
			}
		} else {
			if err := t.waitPastedDraft(ctx, prefix); err != nil {
				return preFailure(err)
			}
		}
		if _, err := t.run(ctx, nil, "paste-buffer", "-p", "-b", buffer, "-t", t.pane); err != nil {
			return fmt.Errorf("Claude delivery uncertain after paste; do not resend: %w", err)
		}
	}
	if err := t.waitPastedDraft(ctx, prefix+message); err != nil {
		return inputFailure("paste_readback_missing", fmt.Errorf("Claude delivery uncertain after paste; no submission shortcut sent, do not resend: %w", err))
	}
	if err := t.verify(ctx, false); err != nil {
		return fmt.Errorf("Claude delivery uncertain after paste; do not resend: %w", err)
	}
	if _, err := t.run(ctx, nil, append([]string{"send-keys", "-t", t.pane}, keys...)...); err != nil {
		return inputFailure("submission_uncertain", fmt.Errorf("Claude delivery uncertain after submission shortcut; do not resend: %w", err))
	}
	return nil
}
