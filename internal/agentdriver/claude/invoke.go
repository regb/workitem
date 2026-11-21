package claude

import (
	"context"
	"os"
	"path/filepath"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agentdriver/oneshot"
)

// neutralInstructions replace Claude Code's system prompt when a request
// brings none: its own would describe a coding agent.
const neutralInstructions = "Answer the user's message directly."

// Invoke sends one prompt to the configured model through `claude --print`:
// no tools, MCP servers, hooks, slash commands or saved session, and the
// request's instructions in place of Claude Code's system prompt.
func Invoke(ctx context.Context, c Config, request protocol.ModelInvokeRequest) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	path := c.Executable
	if path == "" {
		path = "claude"
	}
	instructions := request.Instructions
	if instructions == "" {
		instructions = neutralInstructions
	}
	return oneshot.Run(ctx, oneshot.Command{
		Name: "Claude",
		Path: path,
		Args: func(dir string) ([]string, error) {
			settings := filepath.Join(dir, "settings.json")
			if err := os.WriteFile(settings, []byte(`{"disableAllHooks":true,"autoMemoryEnabled":false}`), 0600); err != nil {
				return nil, err
			}
			args := []string{"--print", "--dangerously-skip-permissions", "--no-session-persistence", "--tools", "", "--disable-slash-commands", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "user", "--settings", settings, "--system-prompt", instructions}
			if c.Model != "" {
				args = append(args, "--model", c.Model)
			}
			if c.Reasoning != "" {
				args = append(args, "--effort", c.Reasoning)
			}
			return args, nil
		},
		Stdin: request.Prompt,
		Drop:  []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_PROJECT_DIR_NAME", "CLAUDE_CODE_ENTRYPOINT"},
	})
}
