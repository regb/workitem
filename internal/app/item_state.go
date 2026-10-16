package app

import (
	"context"

	itemcore "github.com/regb/workitem/internal/app/core/item"
	"github.com/regb/workitem/internal/model"
)

type DeepWorkCapacity = itemcore.Capacity
type StateTransitionResult = itemcore.StateTransitionResult
type WorkItemStateResult = itemcore.StateResult
type BlockerResult = itemcore.BlockerResult

// AddBlockers records waiting intent without touching workspace resources.
func (a *App) AddBlockers(ctx context.Context, opts ResolveOptions, blockers []model.Blocker) (BlockerResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return BlockerResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	return a.itemService().AddBlockers(ctx, opts, blockers)
}

// WorkItemState reads durable lifecycle state without inspecting workspace
// resources or derived attention/agent status.
func (a *App) WorkItemState(ctx context.Context, opts ResolveOptions) (WorkItemStateResult, error) {
	return a.itemService().State(ctx, opts)
}

// SetWorkItemState changes durable lifecycle state only. It intentionally does
// not create, enter, inspect, close, or release workspace resources.
func (a *App) SetWorkItemState(ctx context.Context, opts ResolveOptions, target string, force bool) (StateTransitionResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return StateTransitionResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	return a.itemService().SetState(ctx, opts, target, force, a.DeepWorkConfig.MaxActive)
}

func (a *App) transitionState(ctx context.Context, opts ResolveOptions, target, eventType string, force bool) (StateTransitionResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return StateTransitionResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	return a.itemService().TransitionState(ctx, opts, target, eventType, force, a.DeepWorkConfig.MaxActive)
}
