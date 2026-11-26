package app

import (
	"context"

	itemcore "github.com/regb/workitem/internal/app/core/item"
	"github.com/regb/workitem/internal/model"
)

type DeepWorkCapacity = itemcore.Capacity
type StateTransitionResult = itemcore.StateTransitionResult
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
