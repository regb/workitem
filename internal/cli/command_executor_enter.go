package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func runInternalExecutorEnter(ctx context.Context, args []string, cfg Config) error {
	fs := newFlagSet("internal-executor-enter", cfg.Stderr)
	var executorName, itemID string
	fs.StringVar(&executorName, "executor", "", "executor name")
	fs.StringVar(&itemID, "item", "", "remote work item id")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if len(fs.Args()) != 0 || strings.TrimSpace(executorName) == "" || !model.ValidID(itemID) {
		return usageErr{errors.New("usage: wi internal-executor-enter --executor <name> --item <id>")}
	}
	currentExecutor, currentItem := executorName, itemID
	for {
		if err := cfg.App.AttachExecutorTerminal(ctx, currentExecutor, currentItem); err != nil {
			return err
		}
		handoff, err := cfg.Coordinator.ConsumeExecutorAccess(ctx, currentItem)
		if err != nil {
			return err
		}
		if !handoff.Found {
			return nil
		}
		target, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{Selector: handoff.TargetItemID})
		if err != nil {
			return err
		}
		if target.Checkout.Executor != "" && target.Checkout.Executor != model.ExecutorLocal {
			switch target.State {
			case model.StateWaiting:
				if _, err := coordinatorStartWorkItem(ctx, cfg, target.ID, false, false, agent.ModeTUI, "work_item.resumed"); err != nil {
					return err
				}
			case model.StateBacklog:
				if _, err := coordinatorStartWorkItem(ctx, cfg, target.ID, false, false, agent.ModeTUI, "work_item.started"); err != nil {
					return err
				}
			default:
				if _, err := cfg.App.SwitchWorkItem(ctx, app.ResolveOptions{Selector: target.ID, CWD: cfg.CWD, Env: detachedAccessEnvironment(cfg.Env)}, false); err != nil {
					return err
				}
			}
			currentExecutor, currentItem = target.Checkout.Executor, target.ID
			continue
		}
		local := cfg
		local.Env = detachedAccessEnvironment(cfg.Env)
		switch target.State {
		case model.StateWaiting:
			return startPickerItem(ctx, local, target.ID, "work_item.resumed")
		case model.StateBacklog:
			return startPickerItem(ctx, local, target.ID, "work_item.started")
		default:
			return switchSelectedItem(ctx, local, target.ID, false)
		}
	}
}

func detachedAccessEnvironment(environment map[string]string) map[string]string {
	result := make(map[string]string, len(environment))
	for key, value := range environment {
		switch key {
		case "TMUX", "TMUX_PANE", "WI_ID", "WI_DIR", "WI_WORKTREE", "WI_REPOSITORY", "WI_EXECUTOR", "WI_EXECUTOR_DAEMON_SOCKET", "WI_TMUX_CLIENT":
			continue
		}
		result[key] = value
	}
	return result
}
