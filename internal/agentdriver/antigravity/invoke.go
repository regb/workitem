package antigravity

import (
	"context"
	"slices"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agentdriver/oneshot"
)

// Invoke sends one prompt to the configured model through `agy --print`.
// agy comes less close to a bare model call than other agents: print mode
// cannot turn off its tools or its own system prompt, and agy lists the call
// among its conversations. The empty directory leaves its tools nothing to
// work on, and no permission is granted, so the instructions lead the
// prompt instead. The reasoning level is passed only when set: agy rejects
// a level the model does not offer.
// efforts are the levels `agy --effort` takes.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

func Invoke(ctx context.Context, c Config, request protocol.ModelInvokeRequest) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	path := c.Executable
	if path == "" {
		path = "agy"
	}
	prompt := request.Prompt
	if request.Instructions != "" {
		prompt = request.Instructions + "\n\n" + prompt
	}
	return oneshot.Run(ctx, oneshot.Command{
		Name: "Antigravity",
		Path: path,
		Args: func(string) ([]string, error) {
			args := []string{"--disable-slash-commands", "--print-timeout", "85s"}
			if c.Model != "" {
				args = append(args, "--model", c.Model)
			}
			if slices.Contains(efforts, c.Reasoning) {
				args = append(args, "--effort", c.Reasoning)
			}
			// Attached to the flag: agy would read a prompt starting with
			// "-" as a flag of its own.
			return append(args, "--print="+prompt), nil
		},
		Env: map[string]string{"AGY_CLI_DISABLE_AUTO_UPDATE": "1", "BROWSER": "true"},
	})
}
