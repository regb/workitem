package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/regb/workitem/internal/app"
)

func runSwitch(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("switch", cfg.Stderr)
	var item string
	var current, noAgent, noPreview, take bool
	var labels repeatFlag
	fs.StringVar(&item, "item", "", "work item selector")
	fs.BoolVar(&take, "take", false, "take control from another client, aborting a turn in flight and restarting a headless runtime as a TUI")
	fs.BoolVar(&current, "current", false, "switch to the implicitly resolved current item instead of opening the picker")
	fs.BoolVar(&noAgent, "no-agent", false, "in picker mode, skip live agent/worktree inspection")
	fs.BoolVar(&noPreview, "no-preview", false, "in picker mode, disable the wi show preview")
	fs.Var(&labels, "label", "in picker mode, include/exclude label rule; repeatable")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	hasSelector := selector != ""
	if current && hasSelector {
		return usageErr{errors.New("pass either --current or an item selector, not both")}
	}
	pickerOptions := noAgent || noPreview || len(labels) > 0
	if !current && !hasSelector {
		if jsonOut {
			return usageErr{errors.New("interactive wi switch requires a terminal; pass --current or an item selector with --json")}
		}
		if take {
			return usageErr{errors.New("--take requires --current or an item selector")}
		}
		return runPicker(ctx, cfg, labels, noAgent, noPreview)
	}
	if pickerOptions {
		return usageErr{errors.New("--label, --no-agent, and --no-preview apply only when wi switch opens the picker")}
	}
	return switchSelectedItemWithControl(ctx, cfg, selector, jsonOut, take)
}

func switchSelectedItem(ctx context.Context, cfg Config, selector string, jsonOut bool) error {
	return switchSelectedItemWithControl(ctx, cfg, selector, jsonOut, false)
}

func switchSelectedItemWithControl(ctx context.Context, cfg Config, selector string, jsonOut, take bool) error {
	res, err := cfg.App.SwitchWorkItemWithControl(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}, !jsonOut, take)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	if res.ReadOnly {
		fmt.Fprintf(cfg.Stdout, "switched to %s (read-only)\n", res.WorkItemID)
	} else {
		fmt.Fprintf(cfg.Stdout, "switched to %s\n", res.WorkItemID)
	}
	printCompositionResult(cfg, res)
	return nil
}
