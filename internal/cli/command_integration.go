package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/regb/workitem/internal/app"
)

func runMerge(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("merge", cfg.Stderr)
	var item, target string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.StringVar(&target, "target", "", "local target branch (default: repository default branch)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if fs.NArg() > 1 {
		return usageErr{errors.New("usage: wi merge [--item <selector>] [--target <branch>] [target]")}
	}
	if fs.NArg() == 1 {
		if strings.TrimSpace(target) != "" {
			return usageErr{errors.New("pass target either with --target or positionally, not both")}
		}
		target = fs.Arg(0)
	}
	res, err := cfg.App.MergeWorkItem(ctx, app.MergeOptions{Selector: item, Target: target, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "rebased %s onto %s\n", res.SourceBranch, res.TargetBranch)
	fmt.Fprintf(cfg.Stdout, "merged %s into %s @ %s\n", res.SourceBranch, res.TargetBranch, shortDisplaySHA(res.SourceNewSHA))
	if res.TargetSynced {
		fmt.Fprintf(cfg.Stdout, "synced target worktree: %s\n", res.TargetWorktreePath)
	}
	fmt.Fprintf(cfg.Stdout, "work item remains active: %s; run `wi archive` or `wi shelve` when appropriate\n", res.WorkItemID)
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}

func shortDisplaySHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
