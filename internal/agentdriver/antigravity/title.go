package antigravity

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
	"unicode/utf8"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

// titleTimeout bounds one title request; agy answers in about five seconds.
const titleTimeout = 30 * time.Second

// GenerateTitle asks agy for an item title with a one-shot print request in
// an empty directory, without wi's runtime identity. agy keeps the request in
// its own conversation list; it is not a wi conversation. Only the model is
// set: agy rejects reasoning levels a model does not offer.
func GenerateTitle(ctx context.Context, c Config, prompt string) (string, error) {
	if strings.TrimSpace(prompt) == "" || len(prompt) > 64<<10 {
		return "", fmt.Errorf("bounded prompt required")
	}
	ctx, cancel := context.WithTimeout(ctx, titleTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "wi-agy-title-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := c.Executable
	if path == "" {
		path = "agy"
	}
	// agy has no system prompt flag in print mode: the instruction leads
	// the request, the task follows as data.
	request := protocol.TitleSystemPrompt + "\n\n" + protocol.TitleRequest(prompt)
	args := []string{"--disable-slash-commands", "--print-timeout", "25s"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	// The prompt is attached to the flag: agy would otherwise read a prompt
	// starting with "-" as a flag.
	args = append(args, "--print="+request)
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("")
	// The host's environment without wi's runtime identity or direnv state.
	for _, entry := range launchEnvironment(nil, dir) {
		if !strings.HasPrefix(entry, "WI_") && !strings.HasPrefix(entry, "DIRENV_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	var out, stderr bytes.Buffer
	cmd.Stdout = &limited{buffer: &out}
	cmd.Stderr = &limited{buffer: &stderr}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if line, _, _ := strings.Cut(detail, "\n"); line != "" {
			return "", fmt.Errorf("Antigravity title generation: %w: %s", err, line)
		}
		return "", fmt.Errorf("Antigravity title generation: %w", err)
	}
	title := protocol.CleanTitle(out.String())
	if title == "" || !utf8.ValidString(title) || strings.IndexFunc(title, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("Antigravity returned an invalid title")
	}
	if runes := []rune(title); len(runes) > 96 {
		title = string(runes[:96])
	}
	return title, nil
}

// limited keeps the first 4 KiB written to it.
type limited struct{ buffer *bytes.Buffer }

func (l *limited) Write(p []byte) (int, error) {
	if room := 4096 - l.buffer.Len(); room > 0 {
		if len(p) > room {
			l.buffer.Write(p[:room])
		} else {
			l.buffer.Write(p)
		}
	}
	return len(p), nil
}
