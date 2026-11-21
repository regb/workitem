// Package oneshot runs a model invocation through an agent's command-line
// tool: one process, in an empty directory, with an environment that carries
// no wi runtime identity, its output bounded and its lifetime limited. Each
// driver supplies only the command line and reads its answer from stdout.
package oneshot

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

// Timeout bounds one invocation.
const Timeout = 90 * time.Second

// Command is one invocation of an agent's tool.
type Command struct {
	// Path is the executable.
	Path string
	// Args builds the arguments; dir is the invocation's empty directory,
	// where it may write files the tool reads, such as settings.
	Args func(dir string) ([]string, error)
	// Stdin is the tool's input; most tools read the prompt there.
	Stdin string
	// Env adds variables, such as one that turns auto-updates off.
	Env map[string]string
	// Drop names further variables to leave out, such as a tool's own
	// session identity.
	Drop []string
	// Name names the tool in errors, such as "Claude".
	Name string
}

// dropped are variables no invocation inherits: wi's runtime identity,
// direnv's state, and the terminal multiplexer of whoever started the daemon.
func dropped(key string, extra []string) bool {
	if strings.HasPrefix(key, "WI_") || strings.HasPrefix(key, "DIRENV_") || key == "TMUX" || key == "TMUX_PANE" {
		return true
	}
	for _, name := range extra {
		if key == name {
			return true
		}
	}
	return false
}

// Run runs the command and returns what it printed, trimmed. A failure
// reports the first line the tool wrote to stderr, never the prompt.
func Run(ctx context.Context, c Command) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "wi-invoke-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	args, err := c.Args(dir)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, c.Path, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(c.Stdin)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := c.Env[key]; !replaced && !dropped(key, c.Drop) {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	for key, value := range c.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var stdout, stderr bytes.Buffer
	out := &bounded{buffer: &stdout, limit: protocol.MaxInvokeOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = &bounded{buffer: &stderr, limit: 4096}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s did not answer within %s", c.Name, Timeout)
		}
		if line := firstLine(stderr.String()); line != "" {
			return "", fmt.Errorf("%s: %w: %s", c.Name, err, line)
		}
		return "", fmt.Errorf("%s: %w", c.Name, err)
	}
	if out.overflow {
		return "", fmt.Errorf("%s answered more than %d bytes", c.Name, protocol.MaxInvokeOutputBytes)
	}
	text := strings.TrimSpace(stdout.String())
	if text == "" {
		return "", fmt.Errorf("%s returned no answer", c.Name)
	}
	return text, nil
}

// firstLine is the first non-empty stderr line, without control characters
// and at most 200 characters.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, strings.TrimSpace(line))
		if line == "" {
			continue
		}
		if runes := []rune(line); len(runes) > 200 {
			line = string(runes[:200])
		}
		return line
	}
	return ""
}

// bounded keeps the first limit bytes written to it and notes any more.
type bounded struct {
	buffer   *bytes.Buffer
	limit    int
	overflow bool
}

func (b *bounded) Write(p []byte) (int, error) {
	room := b.limit - b.buffer.Len()
	if len(p) > room {
		b.overflow = true
		if room > 0 {
			b.buffer.Write(p[:room])
		}
		return len(p), nil
	}
	b.buffer.Write(p)
	return len(p), nil
}
