package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/regb/workitem/internal/app"
)

func runShow(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("show", cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.ShowWorkItem(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	m := res.Manifest
	fmt.Fprintf(cfg.Stdout, "ID: %s\n", m.ID)
	slug := m.Slug
	if slug == "" {
		slug = "(none)"
	}
	fmt.Fprintf(cfg.Stdout, "Slug: %s\n", slug)
	fmt.Fprintf(cfg.Stdout, "Title: %s\n", m.Title)
	fmt.Fprintf(cfg.Stdout, "State: %s\n", m.State)
	fmt.Fprintf(cfg.Stdout, "Deep work: %v\n", m.DeepWork)
	if strings.TrimSpace(res.Description) != "" {
		fmt.Fprintf(cfg.Stdout, "Description: %s\n", res.Description)
	}
	fmt.Fprintf(cfg.Stdout, "Labels: %s\n", strings.Join(m.Labels, ", "))
	if repo := res.Repository; repo != nil {
		fmt.Fprintf(cfg.Stdout, "Repository: %s (%s)\n", repo.Name, m.Repository.OperationalRoot())
		switch {
		case repo.UpstreamError != "":
			fmt.Fprintf(cfg.Stdout, "Upstream configuration problem: %s\n", repo.UpstreamError)
		case repo.Upstream != nil:
			fmt.Fprintf(cfg.Stdout, "Upstream: %s\n", repo.Upstream.String())
		default:
			fmt.Fprintf(cfg.Stdout, "Upstream: none (local-only, compared with %s)\n", repo.IntegrationBranch)
		}
	} else {
		fmt.Fprintf(cfg.Stdout, "Repository: %s\n", m.Repository.OperationalRoot())
	}
	if m.Repository.CurrentRoot != "" {
		fmt.Fprintf(cfg.Stdout, "Repository at creation: %s\n", m.Repository.RootAtCreation)
	}
	if m.Repository.RemoteURL != "" {
		fmt.Fprintf(cfg.Stdout, "Remote: %s\n", m.Repository.RemoteURL)
	}
	fmt.Fprintf(cfg.Stdout, "Created from commit: %s\n", m.Repository.CreatedFromCommit)
	fmt.Fprintf(cfg.Stdout, "Workspace kind: %s\n", m.Checkout.Specification.Kind)
	fmt.Fprintf(cfg.Stdout, "Checkout: %s\n", m.Checkout.Presence())
	if m.Checkout.Specification.Branch != "" {
		tracking := ""
		if repo := res.Repository; repo != nil && repo.Ahead != nil {
			tracking = fmt.Sprintf(" (%d ahead, %d behind %s)", *repo.Ahead, *repo.Behind, repo.Base)
		}
		fmt.Fprintf(cfg.Stdout, "Branch: %s%s\n", m.Checkout.Specification.Branch, tracking)
	}
	if m.Checkout.Claim != nil {
		fmt.Fprintf(cfg.Stdout, "Checkout path: %s\n", m.Checkout.Claim.Path)
	}
	fmt.Fprintf(cfg.Stdout, "Terminal: %s %s\n", "tmux", m.TerminalSessionName())
	if m.PrimaryConversation != nil {
		fmt.Fprintf(cfg.Stdout, "Primary conversation: %s\n", m.PrimaryConversation.ID)
		fmt.Fprintf(cfg.Stdout, "Conversation locator: %s\n", m.PrimaryConversation.NativeLocator())
	}
	fmt.Fprintf(cfg.Stdout, "Created: %s\n", m.CreatedAt.Format(timeFormatHuman))
	fmt.Fprintf(cfg.Stdout, "Updated: %s\n", m.UpdatedAt.Format(timeFormatHuman))
	return nil
}

func runEvents(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("events", cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.WorkItemEvents(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	for _, event := range res.Events {
		data, err := json.Marshal(event.Data)
		if err != nil {
			return err
		}
		fmt.Fprintf(cfg.Stdout, "%s  %-28s  %-10s  %s\n", event.Time.Format(timeFormatHuman), event.Type, event.Actor, data)
	}
	return nil
}
