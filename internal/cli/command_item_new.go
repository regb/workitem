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
	var descFile, prompt, slug, base, repoPath, agentMode, agentName, executorName string
	var deep, home, noDefaultLabels bool
	fs.StringVar(&descFile, "desc-file", "", "read work-item description from file")
	fs.StringVar(&prompt, "prompt", "", "use this description and start an agent immediately without attaching")
	fs.StringVar(&agentMode, "agent-mode", "tui", "with --prompt, runtime mode: tui or headless")
	fs.StringVar(&agentName, "agent", "default", "configured agent definition")
	fs.StringVar(&executorName, "executor", "local", "managed-slot executor")
	fs.StringVar(&slug, "slug", "", "explicit active slug for the new work item")
	fs.Var(&labels, "label", "label to add (repeatable)")
	fs.BoolVar(&noDefaultLabels, "no-default-labels", false, "do not inherit user or repository default labels")
	fs.BoolVar(&deep, "deep", false, "mark as deep work")
	fs.BoolVar(&home, "home", false, "borrow the repository's primary default-branch checkout")
	fs.StringVar(&base, "base", "", "base revision")
	fs.StringVar(&repoPath, "repo", "", "repository path for the new work item (default: current directory)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	title := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if title == "" {
		return usageErr{errors.New("usage: wi new [options] <title>")}
	}
	promptSet, agentModeSet := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "prompt":
			promptSet = true
		case "agent-mode":
			agentModeSet = true
		}
	})
	if promptSet && strings.TrimSpace(descFile) != "" {
		return usageErr{errors.New("pass either --prompt or --desc-file, not both")}
	}
	if agentModeSet && !promptSet {
		return usageErr{errors.New("--agent-mode requires --prompt")}
	}
	if cfg.ControlAccess == coordinator.AccessAgent || cfg.ControlAccess == coordinator.AccessExecutor {
		endpoint := cfg.ControlAccess
		if promptSet {
			return fmt.Errorf("%s endpoint may create backlog items only; --prompt is not allowed", endpoint)
		}
		if home {
			return fmt.Errorf("%s endpoint may create managed-slot items only; --home is not allowed", endpoint)
		}
		if cfg.ControlAccess == coordinator.AccessExecutor && strings.TrimSpace(repoPath) != "" {
			return errors.New("executor endpoint creates work in the current authoritative repository; --repo is not allowed")
		}
	}
	description := strings.TrimSpace(prompt)
	if promptSet && description == "" {
		return usageErr{errors.New("--prompt requires a non-empty description")}
	}
	if !promptSet {
		var err error
		description, err = readDescriptionFileFlag(cfg, descFile)
		if err != nil {
			return err
		}
	}
	newCWD := cfg.CWD
	if strings.TrimSpace(repoPath) != "" {
		newCWD = strings.TrimSpace(repoPath)
		if !filepath.IsAbs(newCWD) && cfg.CWD != "" {
			newCWD = filepath.Join(cfg.CWD, newCWD)
		}
	}
	opts := app.NewWorkItemOptions{
		Title: title, Slug: slug, Description: description, Labels: labels,
		NoDefaultLabels: noDefaultLabels, DeepWork: deep, Base: base, Home: home, CWD: newCWD, Env: cfg.Env, Agent: agentName, Executor: executorName,
	}
	if promptSet {
		mode, err := agent.ParseMode(agentMode)
		if err != nil {
			return usageErr{err}
		}
		res, err := runCoordinatorNewAgent(ctx, cfg, opts, mode)
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

func runCoordinatorNewAgent(ctx context.Context, cfg Config, opts app.NewWorkItemOptions, mode agent.Mode) (app.NewAgentWorkItemResult, error) {
	created, err := createCoordinatorWorkItem(ctx, cfg, opts)
	if err != nil {
		return app.NewAgentWorkItemResult{}, err
	}
	result := app.NewAgentWorkItemResult{New: created}
	resolve := app.ResolveOptions{Selector: created.Manifest.ID, CWD: opts.CWD, Env: opts.Env}
	transition, err := coordinatorStateTransition(ctx, cfg, created.Manifest.ID, model.StateWorking, "work_item.started", false)
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
	controlled, err := cfg.App.AgentControl(ctx, app.AgentControlOptions{ResolveOptions: resolve, CommandType: agent.CommandPrompt, Message: strings.TrimSpace(opts.Description), Actor: "operator"})
	result.Control = controlled
	if err != nil {
		return result, fmt.Errorf("created and started work item %s (%s), but could not submit its prompt: %w", created.Manifest.Slug, created.Manifest.ID, err)
	}
	return result, nil
}

func printNewWorkItemResult(cfg Config, res app.NewWorkItemResult) {
	fmt.Fprintf(cfg.Stdout, "created work item %s (%s)\n", res.Manifest.Slug, res.Manifest.ID)
	fmt.Fprintf(cfg.Stdout, "title: %s\n", res.Manifest.Title)
	if res.ItemDir != "" {
		fmt.Fprintf(cfg.Stdout, "item directory: %s\n", res.ItemDir)
	}
	fmt.Fprintf(cfg.Stdout, "repository: %s\n", res.Manifest.Repository.RootAtCreation)
	fmt.Fprintf(cfg.Stdout, "created from commit: %s\n", res.Manifest.Repository.CreatedFromCommit)
	fmt.Fprintf(cfg.Stdout, "branch: %s\n", res.Manifest.Checkout.Specification.Branch)
	fmt.Fprintf(cfg.Stdout, "workspace kind: %s\n", res.Manifest.Checkout.Specification.Kind)
	fmt.Fprintf(cfg.Stdout, "executor: %s\n", res.Manifest.Checkout.Specification.Executor)
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
