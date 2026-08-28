package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/app"
)

func runAttention(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("usage: wi attention <activity|defer|queue> [options]")}
	}
	switch args[0] {
	case "activity":
		return runAttentionActivity(ctx, args[1:], cfg, jsonOut)
	case "defer":
		return runAttentionDefer(ctx, args[1:], cfg, jsonOut)
	case "queue":
		return runAttentionQueue(ctx, args[1:], cfg, jsonOut)
	default:
		return usageErr{fmt.Errorf("unknown attention command %q", args[0])}
	}
}

func runAttentionActivity(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("attention activity", cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	if _, err := cfg.Coordinator.ActivityBarrier(ctx); err != nil {
		return err
	}
	res, err := cfg.App.WorkItemActivity(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "work item: %s\n", res.WorkItemID)
	fmt.Fprintf(cfg.Stdout, "last requested: %s\n", formatOptionalTime(res.Activity.LastRequestedAt))
	fmt.Fprintf(cfg.Stdout, "last completed: %s\n", formatOptionalTime(res.Activity.LastCompletedAt))
	fmt.Fprintf(cfg.Stdout, "last deferred: %s\n", formatOptionalTime(res.Activity.LastDeferredAt))
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}

func runAttentionDefer(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("attention defer", cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	if _, err := cfg.Coordinator.ActivityBarrier(ctx); err != nil {
		return err
	}
	res, err := cfg.App.RecordAttentionDefer(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "deferred %s at %s\n", res.WorkItemID, res.DeferredAt.Format(timeFormatHuman))
	return nil
}

func runAttentionQueue(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("attention queue", cfg.Stderr)
	var labels repeatFlag
	fs.Var(&labels, "label", "include/exclude label rule; repeatable")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 0 {
		return usageErr{errors.New("usage: wi attention queue [--label <rule>]")}
	}
	labelRules, err := effectiveListLabelRules(cfg.App.ListConfig.Labels, cfg.Env, labels)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.AttentionQueue(ctx, app.AttentionQueueOptions{
		ResolveOptions:  app.ResolveOptions{CWD: cfg.CWD, Env: cfg.Env},
		WorkListOptions: app.WorkListOptions{LabelRules: labelRules},
	})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	for _, candidate := range res.Candidates {
		fmt.Fprintf(cfg.Stdout, "%d  %-24s  %s\n", candidate.Rank, itemDisplayName(candidate.Item), candidate.Item.Title)
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}

func formatOptionalTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "(none)"
	}
	return value.Format(timeFormatHuman)
}
