package app

import (
	"context"
	"fmt"

	itemcore "github.com/regb/workitem/internal/app/core/item"
	"github.com/regb/workitem/internal/model"
)

type NewWorkItemOptions struct {
	Title           string
	Slug            string
	Description     string
	Labels          []string
	NoDefaultLabels bool
	DeepWork        bool
	Base            string
	Home            bool
	CWD             string
	Env             map[string]string
}

type NewWorkItemResult struct {
	Manifest model.Manifest `json:"manifest"`
	ItemDir  string         `json:"item_dir"`
	Warnings []string       `json:"warnings"`
}

type PreparedNewWorkItem struct {
	Manifest    model.Manifest
	ItemDir     string
	Warnings    []string
	Description string
}

func (a *App) PrepareNewWorkItem(ctx context.Context, opts NewWorkItemOptions) (PreparedNewWorkItem, error) {
	if a == nil || a.Store == nil || a.Git == nil {
		return PreparedNewWorkItem{}, fmt.Errorf("app is not fully configured")
	}
	res, err := a.itemService().NewWorkItem(ctx, itemcore.NewOptions{
		Title: opts.Title, Slug: opts.Slug, Description: opts.Description,
		Labels: opts.Labels, NoDefaultLabels: opts.NoDefaultLabels,
		DeepWork: opts.DeepWork, Base: opts.Base, Home: opts.Home,
		CWD: opts.CWD, Env: opts.Env, PrepareOnly: true,
	})
	if err != nil {
		return PreparedNewWorkItem{}, err
	}
	return PreparedNewWorkItem{Manifest: res.Manifest, ItemDir: res.ItemDir, Warnings: res.Warnings, Description: res.Description}, nil
}

// NewWorkItem creates only the canonical durable work-item record. It does not
// materialize a worktree, terminal, conversation, or agent runtime.
func (a *App) NewWorkItem(ctx context.Context, opts NewWorkItemOptions) (NewWorkItemResult, error) {
	if a == nil || a.Store == nil || a.Git == nil {
		return NewWorkItemResult{}, fmt.Errorf("app is not fully configured")
	}
	res, err := a.itemService().NewWorkItem(ctx, itemcore.NewOptions{
		Title: opts.Title, Slug: opts.Slug, Description: opts.Description,
		Labels: opts.Labels, NoDefaultLabels: opts.NoDefaultLabels,
		DeepWork: opts.DeepWork, Base: opts.Base, Home: opts.Home,
		CWD: opts.CWD, Env: opts.Env,
	})
	if err != nil {
		return NewWorkItemResult{}, err
	}
	return NewWorkItemResult{Manifest: res.Manifest, ItemDir: res.ItemDir, Warnings: res.Warnings}, nil
}
