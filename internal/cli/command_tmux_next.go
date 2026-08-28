package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/regb/workitem/internal/app"
)

func runNext(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("next", cfg.Stderr)
	var deferCurrent, waitCurrent, archiveCurrent bool
	var labels repeatFlag
	fs.BoolVar(&deferCurrent, "defer", false, "defer the current item if it is in NEEDS ATTENTION before navigating")
	fs.BoolVar(&waitCurrent, "wait", false, "set the current item to waiting before navigating")
	fs.BoolVar(&archiveCurrent, "archive", false, "switch to the next item, then clean up and archive the previous item")
	fs.Var(&labels, "label", "include/exclude label rule for the navigation ring; repeatable")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 0 {
		return usageErr{errors.New("usage: wi next [--defer | --wait | --archive] [--label <rule>]")}
	}
	mutations := 0
	for _, enabled := range []bool{deferCurrent, waitCurrent, archiveCurrent} {
		if enabled {
			mutations++
		}
	}
	if mutations > 1 {
		return usageErr{errors.New("pass only one of --defer, --wait, or --archive")}
	}
	labelRules, err := effectiveListLabelRules(cfg.App.ListConfig.Labels, cfg.Env, labels)
	if err != nil {
		return usageErr{err}
	}
	if archiveCurrent && !jsonOut {
		fmt.Fprintln(cfg.Stdout, "preparing current item for archive before switching...")
	}
	var preWaited *app.StateTransitionResult
	var precomputedQueue *app.AttentionQueueResult
	var precomputedSelection *app.NextQueueSelection
	if waitCurrent {
		current, resolveErr := cfg.App.ResolveItem(ctx, app.ResolveOptions{CWD: cfg.CWD, Env: cfg.Env})
		if resolveErr != nil {
			return fmt.Errorf("cannot wait before navigating: no current work item is selected")
		}
		waited, queue, selection, waitErr := coordinatorWaitAndQueue(ctx, cfg, current.ID, labelRules)
		if waitErr != nil {
			return waitErr
		}
		preWaited = &waited
		precomputedQueue = &queue
		precomputedSelection = selection
	}
	if !waitCurrent && !deferCurrent {
		queue, selection, snapshotErr := coordinatorActionabilitySnapshot(ctx, cfg, labelRules)
		if snapshotErr != nil {
			return fmt.Errorf("load actionability snapshot: %w", snapshotErr)
		}
		precomputedQueue = &queue
		precomputedSelection = selection
	}
	res, err := cfg.App.NextWorkItem(ctx, app.NextWorkItemOptions{
		ResolveOptions:       app.ResolveOptions{CWD: cfg.CWD, Env: cfg.Env},
		WorkListOptions:      app.WorkListOptions{LabelRules: labelRules},
		DeferCurrent:         deferCurrent,
		WaitCurrent:          waitCurrent,
		ArchiveCurrent:       archiveCurrent,
		PreWaited:            preWaited,
		PrecomputedQueue:     precomputedQueue,
		PrecomputedSelection: precomputedSelection,
	}, !jsonOut)
	if err != nil {
		return err
	}
	if res.Selected.Item.Agent == nil {
		res.Warnings = append(res.Warnings, hydrateCoordinatorSelectedItem(ctx, cfg, &res.Selected.Item)...)
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	if res.Deferred != nil {
		fmt.Fprintf(cfg.Stdout, "deferred current item: %s\n", res.Deferred.WorkItemID)
	}
	if res.Archived != nil {
		fmt.Fprintf(cfg.Stdout, "archived previous item: %s\n", res.Archived.WorkItemID)
	}
	if res.Waited != nil {
		if res.Waited.Changed {
			fmt.Fprintf(cfg.Stdout, "set current item to waiting: %s\n", res.Waited.WorkItemID)
		} else {
			fmt.Fprintf(cfg.Stdout, "current item is already waiting: %s\n", res.Waited.WorkItemID)
		}
	}
	fmt.Fprintf(cfg.Stdout, "switched to next NEEDS ATTENTION item: %s\n", itemDisplayName(res.Selected.Item))
	if res.Wrapped {
		fmt.Fprintln(cfg.Stdout, "wrapped to the top of NEEDS ATTENTION")
	} else if !res.CurrentInQueue {
		fmt.Fprintln(cfg.Stdout, "current item is outside NEEDS ATTENTION; started at the top")
	}
	if res.Workspace.Terminal != nil {
		fmt.Fprintf(cfg.Stdout, "terminal: %s\n", res.Workspace.Terminal.Session)
	}
	for _, warning := range append(append([]string{}, res.Warnings...), res.Workspace.Warnings...) {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}
