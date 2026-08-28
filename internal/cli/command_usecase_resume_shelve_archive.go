package cli

import (
	"context"
	"fmt"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func runResume(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("resume", cfg.Stderr)
	var item, agentMode string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.StringVar(&agentMode, "agent-mode", "tui", "agent runtime mode: tui or rpc")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	mode, err := agent.ParseMode(agentMode)
	if err != nil {
		return usageErr{err}
	}
	manifest, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if manifest.State == model.StateBacklog {
		return fmt.Errorf("cannot resume backlog item; run `wi start` first")
	}
	res, err := coordinatorStartWorkItem(ctx, cfg, manifest.ID, false, !jsonOut && mode == agent.ModeTUI, mode, "work_item.resumed")
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	printTransition(cfg.Stdout, res.Transition)
	if res.Workspace.Terminal != nil {
		fmt.Fprintf(cfg.Stdout, "terminal: %s\n", res.Workspace.Terminal.Session)
	}
	if res.Workspace.AgentRuntime != nil {
		fmt.Fprintf(cfg.Stdout, "agent runtime: %s (%s)\n", res.Workspace.AgentRuntime.ID, res.Workspace.AgentRuntime.Mode)
	}
	for _, warning := range res.Workspace.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}

func runShelve(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("shelve", cfg.Stderr)
	var item string
	var force bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.BoolVar(&force, "force", false, "allow shelving while retaining an unclosable current terminal")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.Shelve(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}, force)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	printTransition(cfg.Stdout, res)
	return nil
}

func runArchiveCommand(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("archive", cfg.Stderr)
	var item string
	var force bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.BoolVar(&force, "force", false, "allow archiving while retaining an unclosable current terminal")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.Archive(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}, force)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	printTransition(cfg.Stdout, res)
	return nil
}
