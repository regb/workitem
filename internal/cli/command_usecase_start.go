package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func runStart(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("start", cfg.Stderr)
	var agentMode string
	var force, createNew, home, noDefaultLabels bool
	fs.BoolVar(&createNew, "new", false, "create a minimal item from the title before starting")
	fs.BoolVar(&home, "home", false, "with --new, borrow the repository's primary default-branch checkout")
	fs.BoolVar(&noDefaultLabels, "no-default-labels", false, "do not inherit default labels when creating with --new")
	fs.StringVar(&agentMode, "agent-mode", "tui", "agent runtime mode: tui or rpc")
	fs.BoolVar(&force, "force", false, "override deep work active limit")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if len(fs.Args()) == 0 || !createNew && len(fs.Args()) != 1 {
		return usageErr{errors.New("usage: wi start [--new] [--home] [--no-default-labels] [--agent-mode <tui|rpc>] [--force] <slug-or-title>")}
	}
	input := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if input == "" {
		return usageErr{errors.New("start requires an existing slug or, with --new, a title")}
	}
	if noDefaultLabels && !createNew {
		return usageErr{errors.New("--no-default-labels requires --new")}
	}
	if home && !createNew {
		return usageErr{errors.New("--home requires --new")}
	}
	mode, err := agent.ParseMode(agentMode)
	if err != nil {
		return usageErr{err}
	}
	if createNew {
		if cfg.ControlAccess == coordinator.AccessAgent {
			return errors.New("agent endpoint may create backlog items only; start --new is not allowed")
		}
		created, createErr := createCoordinatorWorkItem(ctx, cfg, app.NewWorkItemOptions{Title: input, CWD: cfg.CWD, Env: cfg.Env, NoDefaultLabels: noDefaultLabels, Home: home})
		if createErr != nil {
			return createErr
		}
		result := app.StartNewWorkItemResult{New: created}
		result.Start, err = coordinatorStartWorkItem(ctx, cfg, created.Manifest.ID, force, !jsonOut && mode == agent.ModeTUI, mode, "work_item.started")
		if err != nil {
			return fmt.Errorf("created work item %s (%s), but could not start it: %w", created.Manifest.Slug, created.Manifest.ID, err)
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		fmt.Fprintf(cfg.Stdout, "created work item %s (%s)\n", result.New.Manifest.Slug, result.New.Manifest.ID)
		printStartResult(cfg, result.Start)
		return nil
	}
	manifest, err := cfg.App.Store.ResolveActiveSlug(input)
	if err != nil {
		return err
	}
	res, err := coordinatorStartWorkItem(ctx, cfg, manifest.ID, force, !jsonOut && mode == agent.ModeTUI, mode, "work_item.started")
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	printStartResult(cfg, res)
	return nil
}

func coordinatorStartWorkItem(ctx context.Context, cfg Config, selector string, force, attach bool, mode agent.Mode, eventType string) (app.StartResult, error) {
	transition, err := coordinatorStateTransition(ctx, cfg, selector, model.StateWorking, eventType, force)
	if err != nil {
		return app.StartResult{}, err
	}
	return cfg.App.MaterializeTransitionAgent(ctx, app.ResolveOptions{Selector: transition.Manifest.ID, CWD: cfg.CWD, Env: cfg.Env}, transition, attach, mode)
}

func printStartResult(cfg Config, res app.StartResult) {
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
}
