package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/regb/workitem/internal/app"
)

func runExecutor(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("usage: wi executor <list|select|doctor|shutdown> [options]")}
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return usageErr{errors.New("usage: wi executor list")}
		}
		result := cfg.App.Executors()
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		if len(result) == 0 {
			fmt.Fprintln(cfg.Stdout, "no executors configured")
			return nil
		}
		for _, executor := range result {
			fmt.Fprintf(cfg.Stdout, "%s\t%s\n", executor.Name, executor.Destination)
		}
		return nil
	case "select":
		fs := newFlagSet("executor select", cfg.Stderr)
		var item string
		fs.StringVar(&item, "item", "", "work item selector")
		if err := fs.Parse(args[1:]); err != nil {
			return usageErr{err}
		}
		if len(fs.Args()) != 1 {
			return usageErr{errors.New("usage: wi executor select --item <selector> <executor>")}
		}
		result, err := cfg.App.SelectExecutor(ctx, app.ResolveOptions{Selector: item, CWD: cfg.CWD, Env: cfg.Env}, fs.Args()[0])
		if err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		if result.Changed {
			fmt.Fprintf(cfg.Stdout, "selected executor %s for %s\n", result.Executor, result.WorkItemID)
		} else {
			fmt.Fprintf(cfg.Stdout, "executor %s is already selected for %s\n", result.Executor, result.WorkItemID)
		}
		return nil
	case "shutdown":
		fs := newFlagSet("executor shutdown", cfg.Stderr)
		var force bool
		fs.BoolVar(&force, "force", false, "confirm stopping all runtimes for this coordinator on the executor")
		if err := fs.Parse(args[1:]); err != nil {
			return usageErr{err}
		}
		if len(fs.Args()) != 1 || !force {
			return usageErr{errors.New("usage: wi executor shutdown --force <name>")}
		}
		if err := cfg.App.ExecutorShutdown(ctx, fs.Args()[0]); err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, map[string]any{"executor": fs.Args()[0], "shutdown": true})
		}
		fmt.Fprintf(cfg.Stdout, "shut down wi resources on executor %s\n", fs.Args()[0])
		return nil
	case "doctor":
		fs := newFlagSet("executor doctor", cfg.Stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return usageErr{err}
		}
		if len(fs.Args()) != 1 {
			return usageErr{errors.New("usage: wi executor doctor <name>")}
		}
		result, err := cfg.App.ExecutorDoctor(ctx, fs.Args()[0])
		if err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		fmt.Fprintf(cfg.Stdout, "executor: %s (%s)\n", result.Executor.Name, result.Executor.Destination)
		fmt.Fprintf(cfg.Stdout, "worker: %s %s/%s\n", result.Worker.WorkerID, result.Worker.GOOS, result.Worker.GOARCH)
		fmt.Fprintf(cfg.Stdout, "wi: %s\n", result.Worker.Version)
		fmt.Fprintf(cfg.Stdout, "compatible: %t\n", result.Compatible)
		if result.Worker.Tmux.SocketPath != "" {
			fmt.Fprintf(cfg.Stdout, "tmux socket: %s\n", result.Worker.Tmux.SocketPath)
			fmt.Fprintf(cfg.Stdout, "tmux config: %s\n", result.Worker.Tmux.ConfigPath)
			fmt.Fprintf(cfg.Stdout, "default shell: %s\n", result.Worker.Tmux.DefaultShell)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
		}
		return nil
	default:
		return usageErr{fmt.Errorf("unknown executor command %q", args[0])}
	}
}
