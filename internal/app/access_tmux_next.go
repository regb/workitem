package app

import (
	"context"
	"errors"
	"fmt"

	tmuxporcelain "github.com/regb/workitem/internal/app/adapter/tmux/porcelain"
	"github.com/regb/workitem/internal/model"
)

type NextQueueSelection struct {
	Index          int
	WorkItemID     string
	CurrentInQueue bool
	Wrapped        bool
}

type NextWorkItemOptions struct {
	ResolveOptions
	WorkListOptions
	DeferCurrent bool
	WaitCurrent  bool
	WaitBlockers []model.Blocker
	CloseCurrent bool
	// CloseResolution records how the closed item ended; empty means completed.
	CloseResolution      string
	PreWaited            *BlockerResult
	PrecomputedQueue     *AttentionQueueResult
	PrecomputedSelection *NextQueueSelection
}

type NextWorkItemResult struct {
	CurrentWorkItemID string                 `json:"current_work_item_id,omitempty"`
	CurrentInQueue    bool                   `json:"current_in_queue"`
	Wrapped           bool                   `json:"wrapped"`
	Deferred          *DeferResult           `json:"deferred,omitempty"`
	Waited            *BlockerResult         `json:"waited,omitempty"`
	Closed            *StateTransitionResult `json:"closed,omitempty"`
	Selected          AttentionCandidate     `json:"selected"`
	Workspace         CompositionResult      `json:"workspace"`
	Warnings          []string               `json:"warnings"`
}

// NextWorkItem is interactive porcelain in the tmux access adapter. It
// composes core attention query/mutation with state-neutral tmux switching;
// neither attention nor lower workspace, terminal, or runtime primitives
// depend on this adapter-level navigation.
func (a *App) NextWorkItem(ctx context.Context, opts NextWorkItemOptions, attach bool) (NextWorkItemResult, error) {
	result := NextWorkItemResult{}
	mutations := 0
	if opts.CloseResolution != "" && !model.ValidResolution(opts.CloseResolution) {
		return result, fmt.Errorf("invalid resolution %q; expected completed, abandoned, or superseded", opts.CloseResolution)
	}
	for _, enabled := range []bool{opts.DeferCurrent, opts.WaitCurrent, opts.CloseCurrent} {
		if enabled {
			mutations++
		}
	}
	if mutations > 1 {
		return result, fmt.Errorf("next can only defer, wait on, or close the current item, not several at once")
	}
	currentID := ""
	if current, err := a.ResolveItem(ctx, opts.ResolveOptions); err == nil {
		currentID = current.ID
	}
	result.CurrentWorkItemID = currentID

	if opts.DeferCurrent && currentID != "" {
		deferred, deferErr := a.DeferWorkItem(ctx, ResolveOptions{Selector: currentID, CWD: opts.CWD, Env: opts.Env})
		if deferErr != nil {
			var eligibilityErr *NotNeedsAttentionError
			if !errors.As(deferErr, &eligibilityErr) {
				return result, deferErr
			}
		} else {
			result.Deferred = &deferred
			result.Warnings = append(result.Warnings, deferred.Warnings...)
		}
	}
	if opts.PreWaited != nil {
		result.Waited = opts.PreWaited
		result.Warnings = append(result.Warnings, opts.PreWaited.Warnings...)
	}
	if opts.WaitCurrent && opts.PreWaited == nil {
		if currentID == "" {
			return result, fmt.Errorf("cannot wait before navigating: no current work item is selected")
		}
		blocked, blockErr := a.AddBlockers(ctx, ResolveOptions{Selector: currentID, CWD: opts.CWD, Env: opts.Env}, opts.WaitBlockers)
		if blockErr != nil {
			return result, blockErr
		}
		result.Waited = &blocked
		result.Warnings = append(result.Warnings, blocked.Warnings...)
	}

	queue := AttentionQueueResult{}
	var err error
	if opts.PrecomputedQueue != nil {
		queue = *opts.PrecomputedQueue
	} else {
		queue, err = a.AttentionQueue(ctx, AttentionQueueOptions{WorkListOptions: opts.WorkListOptions, ResolveOptions: opts.ResolveOptions})
		if err != nil {
			if result.Deferred != nil {
				return result, fmt.Errorf("deferred %s, but could not rebuild attention queue: %w", currentID, err)
			}
			if result.Waited != nil {
				return result, fmt.Errorf("%s is waiting, but could not rebuild attention queue: %w", currentID, err)
			}
			return result, err
		}
	}
	result.Warnings = append(result.Warnings, queue.Warnings...)

	ids := make([]string, len(queue.Candidates))
	for i, candidate := range queue.Candidates {
		ids[i] = candidate.Item.ID
	}
	selection := tmuxporcelain.Selection{}
	if opts.PrecomputedSelection != nil {
		selection = tmuxporcelain.Selection{Index: opts.PrecomputedSelection.Index, CurrentInQueue: opts.PrecomputedSelection.CurrentInQueue, Wrapped: opts.PrecomputedSelection.Wrapped}
		if selection.Index < 0 || selection.Index >= len(queue.Candidates) || queue.Candidates[selection.Index].Item.ID != opts.PrecomputedSelection.WorkItemID {
			return result, fmt.Errorf("daemon next selection does not match the actionability queue")
		}
	} else {
		selection, err = tmuxporcelain.SelectNext(ids, currentID)
	}
	if err != nil {
		if result.Deferred != nil {
			return result, fmt.Errorf("deferred %s, but no next item could be selected: %w", currentID, err)
		}
		if result.Waited != nil {
			return result, fmt.Errorf("%s is waiting, but no next item could be selected: %w", currentID, err)
		}
		return result, err
	}
	result.CurrentInQueue = selection.CurrentInQueue
	result.Wrapped = selection.Wrapped
	result.Selected = queue.Candidates[selection.Index]
	var archiveManifest model.Manifest
	archiveWarnings := []string{}
	if opts.CloseCurrent {
		if currentID == "" {
			return result, fmt.Errorf("cannot close before navigating: no current work item is selected")
		}
		if result.Selected.Item.ID == currentID {
			return result, fmt.Errorf("cannot close current item %s: no distinct next item needs attention", currentID)
		}
		archiveManifest, err = a.Store.LoadManifest(currentID)
		if err != nil {
			return result, err
		}
		archiveWarnings, err = a.prepareArchive(ctx, archiveManifest, ResolveOptions{Selector: currentID, CWD: opts.CWD, Env: opts.Env}, false)
		if err != nil {
			return result, fmt.Errorf("could not prepare current item %s for close before switching: %w", currentID, err)
		}
	}

	workspace, err := a.SwitchWorkItem(ctx, ResolveOptions{Selector: result.Selected.Item.ID, CWD: opts.CWD, Env: opts.Env}, attach)
	if err != nil {
		if result.Deferred != nil {
			return result, fmt.Errorf("deferred %s, but could not switch to %s: %w", currentID, result.Selected.Item.ID, err)
		}
		if result.Waited != nil {
			return result, fmt.Errorf("%s is waiting, but could not switch to %s: %w", currentID, result.Selected.Item.ID, err)
		}
		if opts.CloseCurrent {
			result.Warnings = append(result.Warnings, archiveWarnings...)
			return result, fmt.Errorf("prepared current item %s for close, but could not switch to %s: %w", currentID, result.Selected.Item.ID, err)
		}
		return result, err
	}
	result.Workspace = workspace
	if opts.CloseCurrent {
		archiveEnv := make(map[string]string, len(opts.Env))
		for key, value := range opts.Env {
			archiveEnv[key] = value
		}
		if attach {
			delete(archiveEnv, "TMUX")
			delete(archiveEnv, "TMUX_PANE")
		}
		resolution := opts.CloseResolution
		if resolution == "" {
			resolution = model.ResolutionCompleted
		}
		archived, archiveErr := a.finishArchive(ctx, archiveManifest, archiveManifest.State, resolution, archiveEnv, false, archiveWarnings, attach)
		if archiveErr != nil {
			return result, fmt.Errorf("switched to %s, but could not close previous item %s: %w", result.Selected.Item.ID, currentID, archiveErr)
		}
		result.Closed = &archived
		result.Warnings = append(result.Warnings, archived.Warnings...)
	}
	return result, nil
}
