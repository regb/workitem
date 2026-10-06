package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	pidriver "github.com/regb/workitem/internal/agentdriver/pi"
	"github.com/regb/workitem/internal/cli"
	"github.com/regb/workitem/internal/executor"
	"github.com/regb/workitem/internal/workspacetool"
)

func main() {
	ctx := context.Background()
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "internal-executor" {
		if len(args) != 1 {
			os.Stderr.WriteString("usage: wi internal-executor\n")
			os.Exit(2)
		}
		executorCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
		defer stop()
		if err := executor.Serve(executorCtx, os.Stdin, os.Stdout); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		return
	}
	if len(args) > 0 && args[0] == "internal-workspace-runtime" {
		code, err := workspacetool.RunRuntime(ctx, args[1:], os.Stdin, os.Stdout, os.Stderr)
		if err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
		}
		os.Exit(code)
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
