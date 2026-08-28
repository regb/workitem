package app

import (
	"context"

	itemcore "github.com/regb/workitem/internal/app/core/item"
)

type LabelResult = itemcore.LabelResult
type DeepWorkResult = itemcore.DeepWorkResult

func (a *App) SetDeepWork(ctx context.Context, opts ResolveOptions, deep bool) (DeepWorkResult, error) {
	return a.itemService().SetDeepWork(ctx, opts, deep, a.DeepWorkConfig.MaxActive)
}

func (a *App) AddLabels(ctx context.Context, opts ResolveOptions, labels []string) (LabelResult, error) {
	return a.itemService().AddLabels(ctx, opts, labels)
}

func (a *App) RemoveLabels(ctx context.Context, opts ResolveOptions, labels []string) (LabelResult, error) {
	return a.itemService().RemoveLabels(ctx, opts, labels)
}

func (a *App) ListLabels(ctx context.Context, opts ResolveOptions) (LabelResult, error) {
	return a.itemService().ListLabels(ctx, opts)
}
