package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func runNext(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("next", cfg.Stderr)
	var deferCurrent, waitCurrent, closeCurrent bool
	var conditions waitConditions
	var labels repeatFlag
	fs.BoolVar(&deferCurrent, "defer", false, "defer the current item if it is in NEEDS ATTENTION before navigating")
	fs.BoolVar(&waitCurrent, "wait", false, "make the current item wait before navigating; requires --until, --on, or --for like wi wait")
	conditions.register(fs)
	fs.BoolVar(&closeCurrent, "close", false, "switch to the next item, then clean up and close the previous item")
	var resolution string
	fs.StringVar(&resolution, "resolution", "", "with --close: completed (default), abandoned, or superseded")
	fs.Var(&labels, "label", "include/exclude label rule for the navigation ring; repeatable")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 0 {
		return usageErr{errors.New("usage: wi next [--defer | --wait (--until WHEN | --on ITEM | --for TEXT)... | --close [--resolution R]] [--label <rule>]")}
	}
	if resolution != "" && !closeCurrent {
		return usageErr{errors.New("--resolution requires --close")}
	}
	if resolution != "" && !model.ValidResolution(resolution) {
		return usageErr{fmt.Errorf("invalid resolution %q; expected completed, abandoned, or superseded", resolution)}
	}
	if !conditions.empty() && !waitCurrent {
		return usageErr{errors.New("--until, --on, and --for require --wait")}
	}
	mutations := 0
	for _, enabled := range []bool{deferCurrent, waitCurrent, closeCurrent} {
		if enabled {
			mutations++
		}
	}
	if mutations > 1 {
		return usageErr{errors.New("pass only one of --defer, --wait, or --close")}
	}
	labelRules, err := effectiveListLabelRules(cfg.App.ListConfig.Labels, cfg.Env, labels)
	if err != nil {
		return usageErr{err}
	}
	if closeCurrent && !jsonOut {
		fmt.Fprintln(cfg.Stdout, "preparing current item for close before switching...")
	}
	var preWaited *app.BlockerResult
	var precomputedQueue *app.AttentionQueueResult
	var precomputedSelection *app.NextQueueSelection
	var waitBlockers []model.Blocker
	if waitCurrent {
		waitBlockers, err = conditions.blockers(ctx, cfg)
		if err != nil {
			return err
		}
		current, resolveErr := cfg.App.ResolveItem(ctx, app.ResolveOptions{CWD: cfg.CWD, Env: cfg.Env})
		if resolveErr != nil {
			return fmt.Errorf("cannot wait before navigating: no current work item is selected")
		}
		waited, queue, selection, waitErr := coordinatorWaitAndQueue(ctx, cfg, current.ID, waitBlockers, labelRules)
		if waitErr != nil {
			return waitErr
		}
		preWaited = &waited
		precomputedQueue = &queue
		precomputedSelection = selection
	}
	resolve := app.ResolveOptions{CWD: cfg.CWD, Env: cfg.Env}
	if !waitCurrent && !deferCurrent {
		currentItemID := ""
		if current, err := cfg.App.ResolveItem(ctx, resolve); err == nil {
			currentItemID = current.ID
			// Navigation resolves the current item once.
			resolve.Selector = current.ID
		}
		queue, selection, snapshotErr := coordinatorActionabilitySnapshotFrom(ctx, cfg, currentItemID, labelRules)
		if snapshotErr != nil {
			return fmt.Errorf("load actionability snapshot: %w", snapshotErr)
		}
		precomputedQueue = &queue
		precomputedSelection = selection
	}
	res, err := cfg.App.NextWorkItem(ctx, app.NextWorkItemOptions{
		ResolveOptions:       resolve,
		WorkListOptions:      app.WorkListOptions{LabelRules: labelRules},
		DeferCurrent:         deferCurrent,
		WaitCurrent:          waitCurrent,
		WaitBlockers:         waitBlockers,
		CloseCurrent:         closeCurrent,
		CloseResolution:      resolution,
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
	if res.Closed != nil {
		fmt.Fprintf(cfg.Stdout, "closed previous item: %s\n", res.Closed.WorkItemID)
	}
	if res.Waited != nil {
		for _, blocker := range res.Waited.Added {
			fmt.Fprintf(cfg.Stdout, "current item %s is waiting %s (blocker %s)\n", res.Waited.WorkItemID, blocker.Describe(), blocker.ID)
		}
		if len(res.Waited.Added) == 0 {
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
