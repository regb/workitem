package pi

import (
	"context"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agentdriver/oneshot"
)

// neutralInstructions replace Pi's system prompt when a request brings none.
const neutralInstructions = "Answer the user's message directly."

// Invoke sends one prompt to the configured model through `pi --print`: no
// session, tools, extensions, skills, prompt templates or context files, and
// the request's instructions as the system prompt. Reasoning is off unless
// the config sets it.
func Invoke(ctx context.Context, c Config, request driverprotocol.ModelInvokeRequest) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	path := c.Executable
	if path == "" {
		path = "pi"
	}
	instructions := request.Instructions
	if instructions == "" {
		instructions = neutralInstructions
	}
	thinking := c.Reasoning
	if thinking == "" {
		thinking = "off"
	}
	return oneshot.Run(ctx, oneshot.Command{
		Name: "Pi",
		Path: path,
		Args: func(string) ([]string, error) {
			args := []string{"--print", "--no-session", "--no-tools", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve", "--thinking", thinking, "--system-prompt", instructions}
			if c.Model != "" {
				args = append(args, "--model", c.Model)
			}
			return args, nil
		},
		Stdin: request.Prompt,
		Drop:  []string{"PI_SESSION_ID", "PI_CODING_AGENT_SESSION_DIR"},
	})
}
