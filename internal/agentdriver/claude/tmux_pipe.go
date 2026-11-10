package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// pipeInput bypasses tmux's keyboard-input fence without ever opening it.
// Both paste-buffer and send-keys silently discard input on an input-off pane.
// Refuse an existing pipe rather than replacing an operator's recording pipe.
func (t *tmuxInput) pipeInput(ctx context.Context, data string) error {
	dir, err := os.MkdirTemp("", "wi-claude-input-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "input")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		return err
	}
	if err := t.verify(ctx, false); err != nil {
		return err
	}
	out, err := t.run(ctx, nil, "display-message", "-p", "-t", t.pane, "#{pane_pipe}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "0" {
		return fmt.Errorf("Claude pane already has a pipe; refusing to replace it")
	}
	// Keep the final pipe check in tmux's command queue too. Neither branch
	// enables keyboard input. The private file avoids putting task text in argv.
	command := "cat " + quote(strings.ReplaceAll(strings.ReplaceAll(path, "#", "##"), "%", "%%"))
	guard := fmt.Sprintf("#{&&:#{==:#{pane_pid},%d},#{&&:#{==:#{pane_dead},0},#{&&:#{==:#{pane_in_mode},0},#{==:#{pane_pipe},0}}}}", t.owner)
	if _, err := t.run(ctx, nil, "if-shell", "-F", "-t", t.pane, guard, "pipe-pane -I -t "+t.pane+" "+quote(command), ""); err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// tmux installs the pipe before returning, then removes it on EOF.
	// Wait for EOF before another write, and retain the file until then.
	for {
		out, err := t.run(waitCtx, nil, "display-message", "-p", "-t", t.pane, "#{pane_pipe}")
		if err != nil {
			return err
		}
		if strings.TrimSpace(out) == "0" {
			return nil
		}
		if err := waitEditorPoll(waitCtx); err != nil {
			return err
		}
	}
}

func (t *tmuxInput) paste(ctx context.Context, buffer, text string) error {
	if t.inputOff {
		return t.pipeInput(ctx, "\x1b[200~"+text+"\x1b[201~")
	}
	_, err := t.run(ctx, nil, "paste-buffer", "-p", "-b", buffer, "-t", t.pane)
	return err
}

func (t *tmuxInput) sendKeys(ctx context.Context, keys []string) error {
	if t.inputOff {
		data, err := inputKeyBytes(keys)
		if err != nil {
			return err
		}
		return t.pipeInput(ctx, data)
	}
	_, err := t.run(ctx, nil, append([]string{"send-keys", "-t", t.pane}, keys...)...)
	return err
}

// Only sequences produced by the bounded keybinding resolver are accepted.
func inputKeyBytes(keys []string) (string, error) {
	var out strings.Builder
	special := map[string]string{"Enter": "\r", "Escape": "\x1b", "Tab": "\t", "Space": " ", "BSpace": "\x7f", "BTab": "\x1b[Z", "Up": "\x1b[A", "Down": "\x1b[B", "Right": "\x1b[C", "Left": "\x1b[D", "Home": "\x1b[H", "End": "\x1b[F", "DC": "\x1b[3~", "PPage": "\x1b[5~", "NPage": "\x1b[6~"}
	for _, key := range keys {
		if strings.HasPrefix(key, "M-") {
			out.WriteByte(27)
			key = strings.TrimPrefix(key, "M-")
		}
		if strings.HasPrefix(key, "C-") && len(key) == 3 && key[2] >= 'a' && key[2] <= 'z' {
			out.WriteByte(key[2] - 'a' + 1)
			continue
		}
		if data, ok := special[key]; ok {
			out.WriteString(data)
			continue
		}
		if len(key) == 1 && key[0] >= 33 && key[0] <= 126 {
			out.WriteString(key)
			continue
		}
		return "", fmt.Errorf("Claude keybinding cannot be encoded safely")
	}
	return out.String(), nil
}
