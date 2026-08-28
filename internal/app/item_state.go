package app

import (
	"context"

	itemcore "github.com/regb/workitem/internal/app/core/item"
)

type DeepWorkCapacity = itemcore.Capacity
type StateTransitionResult = itemcore.StateTransitionResult
type WorkItemStateResult = itemcore.StateResult

// WorkItemState reads durable lifecycle state without inspecting workspace
// resources or derived attention/agent status.
func (a *App) WorkItemState(ctx context.Context, opts ResolveOptions) (WorkItemStateResult, error) {
	return a.itemService().State(ctx, opts)
}

// SetWorkItemState changes durable lifecycle state only. It intentionally does
// not create, enter, inspect, close, or release workspace resources.
func (a *App) SetWorkItemState(ctx context.Context, opts ResolveOptions, target string, force bool) (StateTransitionResult, error) {
	return a.itemService().SetState(ctx, opts, target, force, a.DeepWorkConfig.MaxActive)
}

func (a *App) transitionState(ctx context.Context, opts ResolveOptions, target, eventType string, force bool) (StateTransitionResult, error) {
	return a.itemService().TransitionState(ctx, opts, target, eventType, force, a.DeepWorkConfig.MaxActive)
}
