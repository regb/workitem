package main

import (
	"context"
	"os"

	claudedriver "github.com/regb/workitem/internal/agentdriver/claude"
	pidriver "github.com/regb/workitem/internal/agentdriver/pi"
	"github.com/regb/workitem/internal/cli"
)

func main() {
	ctx := context.Background()
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "internal-driver" {
		if len(args) == 4 && args[1] == "claude" && args[2] == "hook" {
			if err := claudedriver.RunHook(ctx, args[3], os.Stdin); err != nil {
				os.Stderr.WriteString(err.Error() + "\n")
				os.Exit(1)
			}
			return
		}
		var run func(context.Context) error
		if len(args) == 2 {
			switch args[1] {
			case "pi":
				run = pidriver.Run
			case "claude":
				run = claudedriver.Run
			}
		}
		if run == nil {
			os.Stderr.WriteString("usage: wi internal-driver <pi|claude>\n")
			os.Exit(2)
		}
		if err := run(ctx); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		return
	}
	os.Exit(cli.Main(ctx, args))
}
