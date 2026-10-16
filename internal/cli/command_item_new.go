package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func runNew(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("new", cfg.Stderr)
	var labels repeatFlag
	var title, descFile, prompt, slug, base, repoPath, agentMode, agentName string
	var deep, noDefaultLabels, start, untitled bool
	fs.StringVar(&title, "title", "", "work-item title; required unless --untitled is supplied")
	fs.BoolVar(&untitled, "untitled", false, "use an editable placeholder title and an ID-derived slug")
	fs.StringVar(&descFile, "desc-file", "", "read work-item description from file")
	fs.BoolVar(&start, "start", false, "start an agent immediately without attaching and send the startup message")
	fs.StringVar(&prompt, "prompt", "", "start an agent and send this message without changing the description")
	fs.StringVar(&agentMode, "agent-mode", "tui", "with --start, runtime mode: tui or headless")
	fs.StringVar(&agentName, "agent", "", "named agent profile (default: default_agent from config)")
	fs.StringVar(&slug, "slug", "", "explicit active slug for the new work item")
	fs.Var(&labels, "label", "label to add (repeatable)")
	fs.BoolVar(&noDefaultLabels, "no-default-labels", false, "do not inherit user or repository default labels")
	fs.BoolVar(&deep, "deep", false, "mark as deep work")
	fs.StringVar(&base, "base", "", "branch, tag, or commit; ^ selects integration, HEAD/@/- use the current worktree")
	fs.StringVar(&repoPath, "repo", "", "registered repository name (default: current work item's repository)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() != 0 {
		return usageErr{errors.New("wi new does not accept positional arguments; use --title TEXT or --untitled")}
	}
	title = strings.TrimSpace(title)
	promptSet, agentModeSet, titleSet, startSet := false, false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "start":
			startSet = true
		case "title":
			titleSet = true
		case "prompt":
			promptSet = true
		case "agent-mode":
			agentModeSet = true
		}
	})
	if untitled && titleSet {
		return usageErr{errors.New("--untitled and --title are mutually exclusive")}
	}
	if !untitled && title == "" {
		return usageErr{errors.New("wi new requires a non-empty --title or --untitled")}
	}
	if promptSet {
		if startSet && !start {
			return usageErr{errors.New("--prompt conflicts with --start=false")}
		}
		start = true
	}
	if agentModeSet && !start {
		return usageErr{errors.New("--agent-mode requires --start")}
	}
	if promptSet && strings.TrimSpace(prompt) == "" {
		return usageErr{errors.New("--prompt requires a non-empty message")}
	}
	mode, err := agent.ParseMode(agentMode)
	if err != nil {
		return usageErr{err}
	}
	if cfg.ControlAccess == coordinator.AccessAgent {
		if start {
			return errors.New("agent endpoint may create backlog items only; --start is not allowed")
		}
	}
	description, err := readDescriptionFileFlag(cfg, descFile)
	if err != nil {
		return err
	}
	newCWD := cfg.CWD
	if strings.TrimSpace(repoPath) != "" {
		newCWD = strings.TrimSpace(repoPath)
		if !filepath.IsAbs(newCWD) && cfg.CWD != "" {
			newCWD = filepath.Join(cfg.CWD, newCWD)
		}
	}
	opts := app.NewWorkItemOptions{
		Title: title, Untitled: untitled, Slug: slug, Description: description, Labels: labels,
		NoDefaultLabels: noDefaultLabels, DeepWork: deep, Base: base, CWD: newCWD, Env: cfg.Env, Agent: agentName,
	}
	if cfg.RequireOwnedRepository {
		selector := strings.TrimSpace(repoPath)
		var current *model.Manifest
		if item, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{CWD: cfg.CWD, Env: cfg.Env}); err == nil {
			current = &item
		}
		if selector == "" {
			if current == nil || current.Repository.ID == "" {
				return usageErr{errors.New("select a registered repository with --repo NAME; use wi repo add NAME --from SOURCE to onboard one")}
			}
			selector = current.Repository.ID
		}
		var metadata coordinator.RepositorySourceResult
		if err := cfg.Coordinator.Call(ctx, coordinator.MethodRepositorySource, coordinator.RepositorySourceRequest{Selector: selector, MetadataOnly: true}, &metadata); err != nil {
			return err
		}
		if metadata.Repository.Archived {
			return fmt.Errorf("repository %q is archived; restore it with wi repo restore %s before creating new items", metadata.Repository.Name, metadata.Repository.Name)
		}
		revision, err := resolveRepositoryBase(ctx, base, cfg.CWD, current, metadata, cfg.App.Git)
		if err != nil {
			return err
		}
		var source coordinator.RepositorySourceResult
		if err := cfg.Coordinator.Call(ctx, coordinator.MethodRepositorySource, coordinator.RepositorySourceRequest{Selector: metadata.Repository.ID, Revision: revision, SkipLabels: noDefaultLabels}, &source); err != nil {
			return err
		}
		opts.RepositoryLabels = source.Labels
		opts.Repository = &model.Repository{ID: source.Repository.ID, Name: source.Repository.Name, IntegrationBranch: source.Repository.IntegrationBranch, RootAtCreation: source.GitDir, GitCommonDir: source.GitDir, CreatedFromCommit: source.Commit}
	}
	if start {
		res, err := runCoordinatorNewAgent(ctx, cfg, opts, mode, cfg.App.AgentStartupMessage(prompt))
		if err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, res)
		}
		printNewWorkItemResult(cfg, res.New)
		printStartResult(cfg, res.Start)
		fmt.Fprintf(cfg.Stdout, "agent command: %s (accepted)\n", res.Control.Command.ID)
		return nil
	}
	res, err := createCoordinatorWorkItem(ctx, cfg, opts)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, newWorkItemCLIResult{
			WorkItemID:      res.Manifest.ID,
			ChangedArtifact: "work_item",
			State:           res.Manifest.State,
			ItemDir:         res.ItemDir,
			Warnings:        res.Warnings,
			Manifest:        &res.Manifest,
			Checkout:        &res.Manifest.Checkout,
		})
	}
	printNewWorkItemResult(cfg, res)
	return nil
}

func runCoordinatorNewAgent(ctx context.Context, cfg Config, opts app.NewWorkItemOptions, mode agent.Mode, message string) (app.NewAgentWorkItemResult, error) {
	if err := cfg.App.ValidateAgentConfig(ctx, opts.Agent); err != nil {
		return app.NewAgentWorkItemResult{}, err
	}
	created, err := createCoordinatorWorkItem(ctx, cfg, opts)
	if err != nil {
		return app.NewAgentWorkItemResult{}, err
	}
	result := app.NewAgentWorkItemResult{New: created}
	resolve := app.ResolveOptions{Selector: created.Manifest.ID, CWD: opts.CWD, Env: opts.Env}
	transition, err := coordinatorStateTransition(ctx, cfg, created.Manifest.ID, model.StateActive, "work_item.started", false)
	if err != nil {
		return result, fmt.Errorf("created work item %s (%s), but could not start its agent: %w", created.Manifest.Slug, created.Manifest.ID, err)
	}
	started, err := cfg.App.MaterializeTransitionAgent(ctx, resolve, transition, false, mode)
	if err != nil {
		return result, fmt.Errorf("created work item %s (%s), but could not start its agent: %w", created.Manifest.Slug, created.Manifest.ID, err)
	}
	result.Start = started
	if err := cfg.App.WaitForAgentRuntimeReady(ctx, created.Manifest.ID, started.Workspace.AgentRuntime); err != nil {
		return result, fmt.Errorf("created and started work item %s (%s), but could not submit its prompt: %w", created.Manifest.Slug, created.Manifest.ID, err)
	}
	controlled, err := cfg.App.AgentControl(ctx, app.AgentControlOptions{ResolveOptions: resolve, CommandType: agent.CommandPrompt, Message: message, Actor: "operator", ExpectedRuntimeID: started.Workspace.AgentRuntime.ID})
	result.Control = controlled
	if err != nil {
		return result, fmt.Errorf("created and started work item %s (%s), but could not submit its prompt: %w", created.Manifest.Slug, created.Manifest.ID, err)
	}
	return result, nil
}

func printNewWorkItemResult(cfg Config, res app.NewWorkItemResult) {
	fmt.Fprintf(cfg.Stdout, "created work item %s (%s)\n", res.Manifest.Slug, res.Manifest.ID)
	fmt.Fprintf(cfg.Stdout, "title: %s\n", res.Manifest.Title)
	fmt.Fprintf(cfg.Stdout, "item directory: %s\n", res.ItemDir)
	fmt.Fprintf(cfg.Stdout, "repository: %s\n", res.Manifest.Repository.RootAtCreation)
	fmt.Fprintf(cfg.Stdout, "created from commit: %s\n", res.Manifest.Repository.CreatedFromCommit)
	if res.Manifest.Checkout.Specification.Branch != "" {
		fmt.Fprintf(cfg.Stdout, "branch: %s\n", res.Manifest.Checkout.Specification.Branch)
	}
	fmt.Fprintf(cfg.Stdout, "workspace kind: %s\n", res.Manifest.Checkout.Specification.Kind)
	fmt.Fprintf(cfg.Stdout, "checkout: %s\n", res.Manifest.Checkout.Presence())
	if res.Manifest.Checkout.Claim != nil {
		fmt.Fprintf(cfg.Stdout, "checkout path: %s\n", res.Manifest.Checkout.Claim.Path)
	}
	if res.Manifest.PrimaryConversation != nil {
		fmt.Fprintf(cfg.Stdout, "primary conversation: %s %s\n", res.Manifest.PrimaryConversation.ID, res.Manifest.PrimaryConversation.NativeLocator())
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
}

func readDescriptionFileFlag(cfg Config, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) && cfg.CWD != "" {
		path = filepath.Join(cfg.CWD, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read --desc-file: %w", err)
	}
	return string(b), nil
}
