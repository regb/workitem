package app

import (
	"context"

	itemcore "github.com/regb/workitem/internal/app/core/item"
	"github.com/regb/workitem/internal/model"
)

type ShowResult struct {
	Manifest    model.Manifest `json:"manifest"`
	Description string         `json:"description"`
}

// ShowWorkItem returns durable metadata with DESCRIPTION.md hydrated. The
// result shape is shared by text and JSON presentation.
func (a *App) ShowWorkItem(ctx context.Context, opts ResolveOptions) (ShowResult, error) {
	res, err := a.itemService().Show(ctx, opts)
	if err != nil {
		return ShowResult{}, err
	}
	return ShowResult{Manifest: res.Manifest, Description: res.Description}, nil
}

type WorkItemEventsResult = itemcore.EventsResult

func (a *App) WorkItemEvents(ctx context.Context, opts ResolveOptions) (WorkItemEventsResult, error) {
	return a.itemService().Events(ctx, opts)
}
