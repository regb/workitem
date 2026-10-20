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
	local := fs.Bool("local", false, "skip upstream fetching and synchronization for an offline local merge")
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
	res, err := cfg.App.MergeWorkItem(ctx, app.MergeOptions{Selector: item, Target: target, CWD: cfg.CWD, Env: cfg.Env, Local: *local})
	if err != nil {
		if res.UpstreamSync != nil && res.UpstreamSync.Changed {
			return fmt.Errorf("%w; successful upstream synchronization retained at %s (recovery ref %s)", err, res.UpstreamSync.LocalSHA, res.UpstreamSync.RecoveryRef)
		}
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	if res.UpstreamSync != nil && res.UpstreamSync.Changed {
		fmt.Fprintf(cfg.Stdout, "synchronized integration base with %s @ %s\n", res.UpstreamSync.Repository.Upstream.String(), shortDisplaySHA(res.UpstreamSync.LocalSHA))
	}
	if res.SourcePreserved {
		fmt.Fprintf(cfg.Stdout, "rebased committed snapshot of %s onto %s; source checkout unchanged\n", res.SourceBranch, res.TargetBranch)
	} else if res.Rebased {
		fmt.Fprintf(cfg.Stdout, "rebased %s onto %s\n", res.SourceBranch, res.TargetBranch)
	}
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
