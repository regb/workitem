package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func runState(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("usage: wi state <show|set> [options]")}
	}
	switch args[0] {
	case "show":
		return runStateShow(ctx, args[1:], cfg, jsonOut)
	case "set":
		return runStateSet(ctx, args[1:], cfg, jsonOut)
	default:
		return usageErr{fmt.Errorf("unknown state command %q", args[0])}
	}
}

func runStateShow(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("state show", cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.WorkItemState(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "%s\n", res.State)
	return nil
}

func runStateSet(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("usage: wi state set <backlog|working|waiting|archived> [--item <selector>] [--force] [item]")}
	}
	target := args[0]
	fs := newFlagSet("state set", cfg.Stderr)
	var item string
	var force bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.BoolVar(&force, "force", false, "override deep-work capacity when setting backlog work to working")
	if err := fs.Parse(args[1:]); err != nil {
		return usageErr{err}
	}
	if target != model.StateWorking && force {
		return usageErr{errors.New("--force is only valid when setting state to working")}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	res, err := coordinatorStateTransition(ctx, cfg, selector, target, "work_item.state_set", force)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	printTransition(cfg.Stdout, res)
	fmt.Fprintln(cfg.Stdout, "workspace unchanged")
	return nil
}
