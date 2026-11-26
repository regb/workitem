// Package wimain is the wi executable's entry point: its command line, and
// the internal-driver commands agent runtimes and drivers run it for.
package wimain

import (
	"context"
	"os"

	antigravitydriver "github.com/regb/workitem/internal/agentdriver/antigravity"
	claudedriver "github.com/regb/workitem/internal/agentdriver/claude"
	pidriver "github.com/regb/workitem/internal/agentdriver/pi"
	"github.com/regb/workitem/internal/cli"
)

// Run runs wi with args, its command line without the program name, and
// returns its exit status. A test binary that stands in for the wi
// executable runs it too.
func Run(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "internal-driver" {
		if len(args) == 4 && args[1] == "claude" && args[2] == "hook" {
			if err := claudedriver.RunHook(ctx, args[3], os.Stdin); err != nil {
				os.Stderr.WriteString(err.Error() + "\n")
				return 1
			}
			return 0
		}
		if len(args) == 4 && args[1] == "claude" && args[2] == "ask" {
			// Never fails the tool call: without an answer, Claude shows its
			// own dialog.
			claudedriver.RunAskHook(ctx, args[3], os.Stdin, os.Stdout)
			return 0
		}
		var run func(context.Context) error
		if len(args) == 2 {
			switch args[1] {
			case "pi":
				run = pidriver.Run
			case "claude":
				run = claudedriver.Run
			case "antigravity":
				run = antigravitydriver.Run
			}
		}
		if run == nil {
			os.Stderr.WriteString("usage: wi internal-driver <pi|claude|antigravity>\n")
			return 2
		}
		if err := run(ctx); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			return 1
		}
		return 0
	}
	return cli.Main(ctx, args)
}
