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
	var current, noAgent, noPreview bool
	var labels repeatFlag
	fs.StringVar(&item, "item", "", "work item selector")
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
		return runPicker(ctx, cfg, labels, noAgent, noPreview)
	}
	if pickerOptions {
		return usageErr{errors.New("--label, --no-agent, and --no-preview apply only when wi switch opens the picker")}
	}
	return switchSelectedItem(ctx, cfg, selector, jsonOut)
}

func switchSelectedItem(ctx context.Context, cfg Config, selector string, jsonOut bool) error {
	res, err := cfg.App.SwitchWorkItem(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}, !jsonOut)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "switched to %s\n", res.WorkItemID)
	printCompositionResult(cfg, res)
	return nil
}
