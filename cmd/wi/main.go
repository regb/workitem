package main

import (
	"context"
	"os"

	pidriver "github.com/regb/workitem/internal/agentdriver/pi"
	"github.com/regb/workitem/internal/cli"
	"github.com/regb/workitem/internal/executor"
)

func main() {
	ctx := context.Background()
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "internal-executor-attach" {
		if len(args) != 3 {
			os.Stderr.WriteString("usage: wi internal-executor-attach <master-id> <item-id>\n")
			os.Exit(2)
		}
		if err := executor.AttachAgentTerminal(ctx, args[1], args[2]); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && args[0] == "internal-executor-shell" {
		if len(args) != 4 {
			os.Stderr.WriteString("usage: wi internal-executor-shell <master-id> <item-id> <gateway-pane-id>\n")
			os.Exit(2)
		}
		if err := executor.AttachShellTerminal(ctx, args[1], args[2], args[3]); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && args[0] == "internal-executor" {
		if len(args) != 1 {
			os.Stderr.WriteString("usage: wi internal-executor\n")
			os.Exit(2)
		}
		if err := executor.Serve(ctx, os.Stdin, os.Stdout); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && args[0] == "internal-driver" {
		if len(args) != 2 || args[1] != "pi" {
			os.Stderr.WriteString("usage: wi internal-driver pi\n")
			os.Exit(2)
		}
		if err := pidriver.Run(ctx); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		return
	}
	os.Exit(cli.Main(ctx, args))
}
