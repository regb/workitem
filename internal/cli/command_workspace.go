package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/regb/workitem/internal/app"
)

func runWorkspace(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("usage: wi workspace <status|ensure|release|relocate> [options] [item]")}
	}
	switch args[0] {
	case "status":
		return runWorkspaceStatus(ctx, args[1:], cfg, jsonOut)
	case "ensure":
		return runWorkspaceEnsure(ctx, args[1:], cfg, jsonOut)
	case "release":
		return runWorkspaceRelease(ctx, args[1:], cfg, jsonOut)
	case "relocate":
		return runWorkspaceRelocate(ctx, args[1:], cfg, jsonOut)
	default:
		return usageErr{fmt.Errorf("unknown workspace command %q", args[0])}
	}
}

func runWorkspaceStatus(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("workspace status", cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.WorkspaceStatus(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "work item: %s\n", res.WorkItemID)
	fmt.Fprintf(cfg.Stdout, "state: %s\n", res.State)
	fmt.Fprintf(cfg.Stdout, "workspace kind: %s\n", res.Checkout.Kind)
	fmt.Fprintf(cfg.Stdout, "checkout: %s\n", res.Checkout.Presence())
	if res.Checkout.Path != nil {
		fmt.Fprintf(cfg.Stdout, "checkout path: %s\n", *res.Checkout.Path)
	}
	status, reason := workspaceDiagnostic(res)
	fmt.Fprintf(cfg.Stdout, "worktree: %s\n", status)
	if reason != "" {
		fmt.Fprintf(cfg.Stdout, "reason: %s\n", reason)
	}
	if res.Git.ExpectedBranch != "" {
		fmt.Fprintf(cfg.Stdout, "expected branch: %s\n", res.Git.ExpectedBranch)
	}
	if res.Git.CurrentBranch != "" {
		fmt.Fprintf(cfg.Stdout, "current branch: %s\n", res.Git.CurrentBranch)
	}
	if res.Manifest.Repository.CreatedFromCommit != "" {
		fmt.Fprintf(cfg.Stdout, "created from commit: %s\n", res.Manifest.Repository.CreatedFromCommit)
	}
	if res.Git.CurrentHead != "" {
		fmt.Fprintf(cfg.Stdout, "current HEAD: %s\n", res.Git.CurrentHead)
	}
	fmt.Fprintf(cfg.Stdout, "dirty: %t\n", res.Git.Dirty)
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}

func workspaceDiagnostic(res app.WorkspaceStatusResult) (string, string) {
	if !res.Checkout.Present() || res.Checkout.Path == nil || *res.Checkout.Path == "" {
		return "absent", "checkout is absent"
	}
	if res.Git.BranchMismatch {
		return "problem", fmt.Sprintf("checkout branch %s differs from expected %s", res.Git.CurrentBranch, res.Git.ExpectedBranch)
	}
	if len(res.Warnings) > 0 {
		return "problem", res.Warnings[0]
	}
	if res.Git.Dirty {
		return "changed", "checkout has uncommitted changes"
	}
	if res.Git.CurrentHead != "" && res.Manifest.Repository.CreatedFromCommit != "" && res.Git.CurrentHead != res.Manifest.Repository.CreatedFromCommit {
		return "changed", "checkout HEAD differs from the item's created-from commit"
	}
	return "clean", "checkout matches the expected branch and created-from commit"
}

func runWorkspaceRelocate(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("workspace relocate", cfg.Stderr)
	var item, repository string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.StringVar(&repository, "repository", "", "replacement repository checkout path")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if repository == "" {
		return usageErr{errors.New("--repository is required")}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.RelocateWorkItemRepository(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}, repository)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	if res.Changed {
		fmt.Fprintf(cfg.Stdout, "relocated repository for %s\n", res.WorkItemID)
	} else {
		fmt.Fprintf(cfg.Stdout, "repository already located for %s\n", res.WorkItemID)
	}
	fmt.Fprintf(cfg.Stdout, "repository: %s\n", res.CurrentRoot)
	return nil
}

func runWorkspaceEnsure(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	selector, err := workspaceSelector("workspace ensure", args, cfg)
	if err != nil {
		return err
	}
	res, err := cfg.App.EnsureWorkItemWorkspace(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "ensured workspace for %s\n", res.WorkItemID)
	printCompositionResult(cfg, res)
	return nil
}

func runWorkspaceRelease(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("workspace release", cfg.Stderr)
	var item string
	var force bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.BoolVar(&force, "force", false, "pass through to managed worktree release where safe")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.ReleaseWorkItemWorkspace(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}, force)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	if res.Changed {
		fmt.Fprintf(cfg.Stdout, "released workspace for %s\n", res.WorkItemID)
	} else {
		fmt.Fprintf(cfg.Stdout, "workspace already released for %s\n", res.WorkItemID)
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}

func workspaceSelector(command string, args []string, cfg Config) (string, error) {
	fs := newFlagSet(command, cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return "", usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return "", usageErr{err}
	}
	return selector, nil
}

func printCompositionResult(cfg Config, res app.CompositionResult) {
	if res.Checkout.Path != nil {
		fmt.Fprintf(cfg.Stdout, "checkout: %s\n", *res.Checkout.Path)
	}
	if res.Terminal != nil {
		fmt.Fprintf(cfg.Stdout, "terminal: %s\n", res.Terminal.Session)
	}
	if res.AgentRuntime != nil {
		fmt.Fprintf(cfg.Stdout, "agent runtime: %s (%s)\n", res.AgentRuntime.ID, res.AgentRuntime.Mode)
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
}
