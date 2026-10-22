package pi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

// GenerateTitle runs an isolated, non-persisted Pi print request. It has no
// tools, project instructions, extensions, or access to an item's conversation.
func GenerateTitle(ctx context.Context, config Config, prompt string) (string, error) {
	if strings.TrimSpace(prompt) == "" || len(prompt) > 64<<10 {
		return "", fmt.Errorf("bounded prompt required")
	}
	// Bound the auxiliary model request independently of its caller.
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	path := config.Executable
	if path == "" {
		path = "pi"
	}
	dir, err := os.MkdirTemp("", "wi-title-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, path, "--print", "--no-session", "--no-tools", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve", "--thinking", "off", "--system-prompt", driverprotocol.TitleSystemPrompt)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(driverprotocol.TitleRequest(prompt))
	// Do not let this auxiliary process impersonate a primary runtime.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "WI_") || strings.HasPrefix(key, "DIRENV_") || key == "TMUX" || key == "TMUX_PANE" || key == "PI_SESSION_ID" || key == "PI_CODING_AGENT_SESSION_DIR" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	output := &titleOutput{}
	cmd.Stdout = output
	cmd.Stderr = &titleOutput{}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("Pi title generation: %w", err)
	}
	title := driverprotocol.CleanTitle(output.text)
	if title == "" || output.overflow || !utf8.ValidString(title) || strings.IndexFunc(title, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("Pi returned an invalid title")
	}
	runes := []rune(title)
	if len(runes) > 96 {
		title = string(runes[:96])
	}
	return title, nil
}

// Bound both outputs without ever logging the prompt or model diagnostics.
type titleOutput struct {
	text     string
	overflow bool
}

func (b *titleOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.text)+n > 4096 {
		b.overflow = true
	}
	if available := 4096 - len(b.text); available > 0 {
		if len(p) > available {
			p = p[:available]
		}
		b.text += string(p)
	}
	return n, nil
}
