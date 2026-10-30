package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

// ReopenResult is a reopened item, and its start unless it stayed in the
// backlog.
type ReopenResult struct {
	Reopened app.StateTransitionResult `json:"reopened"`
	Started  *app.StartResult          `json:"started,omitempty"`
}

// runReopen returns a closed item to work. Reopening is for going back to
// it, so the item starts as wi start would, unless --backlog keeps it there.
func runReopen(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("reopen", cfg.Stderr)
	var item, agentMode, modelName, reasoning string
	var backlog, force bool
	fs.StringVar(&item, "item", "", "closed work item ID")
	fs.BoolVar(&backlog, "backlog", false, "return the item to the backlog without starting it")
	fs.StringVar(&agentMode, "agent-mode", "tui", "agent runtime mode: tui or headless")
	fs.StringVar(&modelName, "model", "", "set the item's model before starting")
	fs.StringVar(&reasoning, "reasoning", "", "set the item's reasoning level before starting")
	fs.BoolVar(&force, "force", false, "override deep work active limit")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	if selector == "" {
		return usageErr{errors.New("usage: wi reopen [--backlog] [--agent-mode <tui|headless>] [--model <model>] [--reasoning <level>] [--force] <item-id>")}
	}
	mode, err := agent.ParseMode(agentMode)
	if err != nil {
		return usageErr{err}
	}
	if backlog && (modelName != "" || reasoning != "" || force) {
		return usageErr{errors.New("--model, --reasoning and --force start the item; they do not combine with --backlog")}
	}
	opts := app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}
	state, err := cfg.App.WorkItemState(ctx, opts)
	if err != nil {
		return err
	}
	if state.State != model.StateClosed {
		return fmt.Errorf("work item %s is %s, not closed; start a backlog item with wi start", state.WorkItemID, state.State)
	}
	reopened, err := coordinatorStateTransition(ctx, cfg, selector, model.StateBacklog, "work_item.state_set", false)
	if err != nil {
		return err
	}
	result := ReopenResult{Reopened: reopened}
	if !backlog {
		started, err := startReopened(ctx, cfg, reopened.Manifest, modelName, reasoning, force, !jsonOut && mode == agent.ModeTUI, mode)
		if err != nil {
			// The item stays reopened; say so rather than roll it back.
			return fmt.Errorf("reopened %s in the backlog, but starting it failed (retry with wi start %s): %w", reopened.Manifest.ID, reopened.Manifest.Slug, err)
		}
		result.Started = &started
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, result)
	}
	printTransition(cfg.Stdout, reopened)
	if result.Started != nil {
		printStartResult(cfg, *result.Started)
	}
	return nil
}

func startReopened(ctx context.Context, cfg Config, m model.Manifest, modelName, reasoning string, force, attach bool, mode agent.Mode) (app.StartResult, error) {
	if settings := agentSettingFlags(modelName, reasoning); len(settings) > 0 {
		if _, err := cfg.App.SetAgentSettings(ctx, app.SetAgentSettingsOptions{ResolveOptions: app.ResolveOptions{Selector: m.ID, CWD: cfg.CWD, Env: cfg.Env}, Settings: settings}); err != nil {
			return app.StartResult{}, err
		}
	}
	return coordinatorStartWorkItem(ctx, cfg, m.ID, force, attach, mode, "work_item.started")
}
