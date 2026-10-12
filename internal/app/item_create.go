package app

import (
	"context"
	"fmt"
	"strings"

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
	Agent           string
	ExecutorProfile string
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
	executorProfile, err := a.resolveExecutorProfile(opts.ExecutorProfile, opts.Home)
	if err != nil {
		return PreparedNewWorkItem{}, err
	}
	res, err := a.itemService().NewWorkItem(ctx, itemcore.NewOptions{
		Title: opts.Title, Slug: opts.Slug, Description: opts.Description, Agent: agentName,
		Labels: opts.Labels, NoDefaultLabels: opts.NoDefaultLabels,
		DeepWork: opts.DeepWork, Base: opts.Base, Home: opts.Home,
		CWD: opts.CWD, Env: opts.Env, PrepareOnly: true, ExecutorProfile: executorProfile,
	})
	if err != nil {
		return PreparedNewWorkItem{}, err
	}
	res.Warnings = a.remoteSourceCheckoutWarnings(ctx, res.Manifest, res.Warnings)
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
	executorProfile, err := a.resolveExecutorProfile(opts.ExecutorProfile, opts.Home)
	if err != nil {
		return NewWorkItemResult{}, err
	}
	res, err := a.itemService().NewWorkItem(ctx, itemcore.NewOptions{
		Title: opts.Title, Slug: opts.Slug, Description: opts.Description, Agent: agentName,
		Labels: opts.Labels, NoDefaultLabels: opts.NoDefaultLabels,
		DeepWork: opts.DeepWork, Base: opts.Base, Home: opts.Home,
		CWD: opts.CWD, Env: opts.Env, ExecutorProfile: executorProfile,
	})
	if err != nil {
		return NewWorkItemResult{}, err
	}
	res.Warnings = a.remoteSourceCheckoutWarnings(ctx, res.Manifest, res.Warnings)
	return NewWorkItemResult{Manifest: res.Manifest, ItemDir: res.ItemDir, Warnings: res.Warnings}, nil
}

func (a *App) remoteSourceCheckoutWarnings(ctx context.Context, manifest model.Manifest, warnings []string) []string {
	if manifest.Checkout.Specification.Kind != model.WorkspaceKindExecutor {
		return warnings
	}
	status, err := a.Git.StatusPorcelain(ctx, manifest.Repository.OperationalRoot())
	if err != nil {
		return append(warnings, "could not inspect the local checkout for changes before remote execution: "+err.Error())
	}
	if strings.TrimSpace(status) == "" {
		return warnings
	}
	commit := strings.TrimSpace(manifest.Repository.CreatedFromCommit)
	if len(commit) > 12 {
		commit = commit[:12]
	}
	return append(warnings, fmt.Sprintf("local checkout has uncommitted changes; executor workspace with profile %q starts from commit %s and does not include staged, unstaged, or untracked files", manifest.Checkout.Specification.ExecutorProfile, commit))
}

func (a *App) resolveExecutorProfile(value string, home bool) (string, error) {
	if value = strings.TrimSpace(value); value == "" {
		value = ""
	}
	if value == "" {
		return value, nil
	}
	if home {
		return "", fmt.Errorf("--home and --executor are mutually exclusive")
	}
	if _, ok := a.ExecutorsConfig[value]; !ok {
		return "", fmt.Errorf("executor profile %q is not configured", value)
	}
	return value, nil
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
