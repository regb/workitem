package app

import (
	"context"
	"fmt"

	itemcore "github.com/regb/workitem/internal/app/core/item"
	"github.com/regb/workitem/internal/model"
)

type NewWorkItemOptions struct {
	Repository       *model.Repository
	RepositoryLabels []string
	Title            string
	Untitled         bool
	Slug             string
	Description      string
	Labels           []string
	NoDefaultLabels  bool
	DeepWork         bool
	Base             string
	CWD              string
	Env              map[string]string
	Agent            string
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
	agentName, err := a.resolveAgentName(opts.Agent)
	if err != nil {
		return PreparedNewWorkItem{}, err
	}
	res, err := a.itemService().NewWorkItem(ctx, itemcore.NewOptions{
		Title: opts.Title, Untitled: opts.Untitled, Slug: opts.Slug, Description: opts.Description, Agent: agentName,
		Labels: opts.Labels, NoDefaultLabels: opts.NoDefaultLabels,
		DeepWork: opts.DeepWork, Base: opts.Base,
		CWD: opts.CWD, Env: opts.Env, PrepareOnly: true, Repository: opts.Repository, RepositoryLabels: opts.RepositoryLabels,
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
	agentName, err := a.resolveAgentName(opts.Agent)
	if err != nil {
		return NewWorkItemResult{}, err
	}
	res, err := a.itemService().NewWorkItem(ctx, itemcore.NewOptions{
		Title: opts.Title, Untitled: opts.Untitled, Slug: opts.Slug, Description: opts.Description, Agent: agentName,
		Labels: opts.Labels, NoDefaultLabels: opts.NoDefaultLabels,
		DeepWork: opts.DeepWork, Base: opts.Base,
		CWD: opts.CWD, Env: opts.Env, Repository: opts.Repository, RepositoryLabels: opts.RepositoryLabels,
	})
	if err != nil {
		return NewWorkItemResult{}, err
	}
	return NewWorkItemResult{Manifest: res.Manifest, ItemDir: res.ItemDir, Warnings: res.Warnings}, nil
}

func (a *App) resolveAgentName(value string) (string, error) {
	if value == "" {
		value = "default"
	}
	definition, ok := a.AgentsConfig[value]
	if !ok {
		return "", fmt.Errorf("agent definition %q is not configured", value)
	}
	if definition.Driver == "" {
		return "", fmt.Errorf("agent definition %q has no driver", value)
	}
	if _, ok := a.DriversConfig[definition.Driver]; !ok {
		return "", fmt.Errorf("agent definition %q uses unconfigured driver %q", value, definition.Driver)
	}
	return value, nil
}
