package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func runStart(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("start", cfg.Stderr)
	var agentMode, modelName, reasoning string
	var force bool
	fs.StringVar(&agentMode, "agent-mode", "tui", "agent runtime mode: tui or headless")
	fs.StringVar(&modelName, "model", "", "set the item's model before starting")
	fs.StringVar(&reasoning, "reasoning", "", "set the item's reasoning level before starting")
	fs.BoolVar(&force, "force", false, "override deep work active limit")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 1 {
		return usageErr{errors.New("usage: wi start [--agent-mode <tui|headless>] [--model <model>] [--reasoning <level>] [--force] <slug>")}
	}
	input := strings.TrimSpace(fs.Arg(0))
	if input == "" {
		return usageErr{errors.New("start requires an existing active slug")}
	}
	mode, err := agent.ParseMode(agentMode)
	if err != nil {
		return usageErr{err}
	}
	manifest, err := cfg.App.Store.ResolveActiveSlug(input)
	if err != nil {
		return err
	}
	// The choices are durable intent for this and every later runtime, so
	// they are saved even if the start then fails.
	if settings := agentSettingFlags(modelName, reasoning); len(settings) > 0 {
		if _, err := cfg.App.SetAgentSettings(ctx, app.SetAgentSettingsOptions{ResolveOptions: app.ResolveOptions{Selector: manifest.ID, CWD: cfg.CWD, Env: cfg.Env}, Settings: settings}); err != nil {
			return err
		}
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
	if err := cfg.App.ValidateItemAgentConfig(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}); err != nil {
		return app.StartResult{}, err
	}
	transition, err := coordinatorStateTransition(ctx, cfg, selector, model.StateActive, eventType, force)
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
