package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func writeRepositoryStatus(cfg Config, status coordinator.RepositoryStatusResult) {
	fmt.Fprintf(cfg.Stdout, "%s\nIntegration branch: %s @ %s\n", status.Repository.Name, status.Repository.IntegrationBranch, shortDisplaySHA(status.LocalSHA))
	if status.Repository.UpstreamError != "" {
		fmt.Fprintf(cfg.Stdout, "Upstream configuration problem: %s\n", status.Repository.UpstreamError)
	} else if status.Repository.Upstream == nil {
		fmt.Fprintln(cfg.Stdout, "Upstream: none (local-only)")
	} else {
		fmt.Fprintf(cfg.Stdout, "Upstream: %s\nState: %s (%d ahead, %d behind)\n", status.Repository.Upstream.String(), status.State, status.Ahead, status.Behind)
		if status.FetchedAt != nil {
			fmt.Fprintf(cfg.Stdout, "Last wi fetch: %s (cached observation)\n", status.FetchedAt.Format(time.RFC3339))
		} else {
			fmt.Fprintln(cfg.Stdout, "Last wi fetch: unknown; use --fetch to inspect the remote")
		}
		if status.State == "diverged" && status.EqualTrees {
			fmt.Fprintln(cfg.Stdout, "File trees are identical; commit histories differ.")
		}
	}
	if status.Pending != nil {
		fmt.Fprintf(cfg.Stdout, "Pending synchronization: %s (%s)\n", status.Pending.Mode, status.Pending.Phase)
		if status.ReconciliationPath != "" {
			fmt.Fprintf(cfg.Stdout, "Reconciliation checkout: %s\n", status.ReconciliationPath)
		}
	}
}

func runRepositoryRemote(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	verb := args[0]
	// Accept positional arguments before or after flags. --branch is the
	// only value-taking option; keep its value attached during reordering.
	var flags, positionals []string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if arg == "--branch" && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positionals = append(positionals, arg)
		}
	}
	args = append(append([]string{verb}, flags...), positionals...)
	fs := newFlagSet("repo "+verb, cfg.Stderr)
	fetch := fs.Bool("fetch", false, "fetch upstream before inspecting status")
	clear := fs.Bool("clear", false, "remove the optional upstream")
	branch := fs.String("branch", "", "upstream branch; defaults to the integration branch")
	merge := fs.Bool("merge", false, "reconcile divergent history in a dedicated checkout")
	reset := fs.Bool("reset-to-upstream", false, "replace local integration tip, retaining a recovery ref")
	yes := fs.Bool("yes", false, "confirm replacing the integration tip with the fetched upstream")
	resume := fs.Bool("continue", false, "finish pending synchronization after resolving conflicts")
	abort := fs.Bool("abort", false, "abort pending reconciliation without advancing integration")
	if err := fs.Parse(args[1:]); err != nil {
		return usageErr{err}
	}
	if verb == "upstream" {
		if *fetch || *merge || *reset || *yes || *resume || *abort || (!*clear && (fs.NArg() < 1 || fs.NArg() > 2)) || (*clear && (fs.NArg() > 1 || *branch != "")) {
			return usageErr{fmt.Errorf("usage: wi repo upstream [NAME] URL [--branch BRANCH], or [NAME] --clear")}
		}
		name, value := "", ""
		if *clear {
			name = fs.Arg(0)
		} else {
			value = fs.Arg(fs.NArg() - 1)
			if fs.NArg() == 2 {
				name = fs.Arg(0)
			}
		}
		name, err := repositorySelector(ctx, cfg, verb, name)
		if err != nil {
			return err
		}
		source, err := cfg.Coordinator.RepositoryViewSource(ctx, name, "")
		if err != nil {
			return err
		}
		var upstream *model.RepositoryUpstream
		if !*clear {
			if *branch == "" {
				*branch = source.Repository.IntegrationBranch
			}
			upstream, err = model.ParseRepositoryUpstream(value, *branch)
			if err != nil {
				return usageErr{err}
			}
		}
		id, err := model.NewID()
		if err != nil {
			return err
		}
		result, err := cfg.Coordinator.SetRepositoryUpstream(ctx, coordinator.SetRepositoryUpstreamCommand{ID: id, ProtocolVersion: coordinator.ProtocolVersion, RepositoryID: source.Repository.ID, PreviousUpstream: source.Repository.Upstream, Upstream: upstream, CreatedAt: time.Now().UTC()})
		if err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		if upstream == nil {
			fmt.Fprintf(cfg.Stdout, "repository %s is local-only\n", result.Repository.Name)
		} else {
			fmt.Fprintf(cfg.Stdout, "repository %s tracks %s\n", result.Repository.Name, upstream.String())
		}
		return nil
	}
	if fs.NArg() > 1 || *clear || *branch != "" {
		return usageErr{fmt.Errorf("usage: wi repo %s [NAME] [options]", verb)}
	}
	name, err := repositorySelector(ctx, cfg, verb, fs.Arg(0))
	if err != nil {
		return err
	}
	if verb == "status" {
		if *merge || *reset || *yes || *resume || *abort {
			return usageErr{fmt.Errorf("repo status accepts only --fetch")}
		}
		result, err := cfg.Coordinator.RepositoryStatus(ctx, coordinator.RepositoryStatusRequest{Selector: name, Fetch: *fetch})
		if err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		writeRepositoryStatus(cfg, result)
		return nil
	}
	if *fetch {
		return usageErr{fmt.Errorf("repo sync always fetches; --fetch is only for repo status")}
	}
	modes := 0
	mode := "ff"
	for _, choice := range []struct {
		flag bool
		mode string
	}{{*merge, "merge"}, {*reset, "reset"}, {*resume, "continue"}, {*abort, "abort"}} {
		if choice.flag {
			modes++
			mode = choice.mode
		}
	}
	if modes > 1 || (*yes && !*reset) {
		return usageErr{fmt.Errorf("choose only one of --merge, --reset-to-upstream, --continue, --abort; --yes requires --reset-to-upstream")}
	}
	request := coordinator.RepositorySyncRequest{Selector: name, Mode: mode}
	if *reset {
		if !*yes {
			return usageErr{fmt.Errorf("reset replaces local integration history; inspect wi repo status %s --fetch, then pass --yes to confirm; item branches and the original checkout remain untouched", name)}
		}
		status, err := cfg.Coordinator.RepositoryStatus(ctx, coordinator.RepositoryStatusRequest{Selector: name, Fetch: true})
		if err != nil {
			return err
		}
		if status.Pending != nil {
			return fmt.Errorf("finish or abort pending synchronization before resetting")
		}
		request.ConfirmReset = true
		request.ExpectedLocalSHA = status.LocalSHA
		request.ExpectedUpstreamSHA = status.UpstreamSHA
		for _, sha := range status.LocalOnlyCommits {
			fmt.Fprintf(cfg.Stderr, "Local-only commit leaving %s: %s\n", status.Repository.IntegrationBranch, sha)
		}
		if status.Ahead > len(status.LocalOnlyCommits) {
			fmt.Fprintf(cfg.Stderr, "... and %d more local-only commits\n", status.Ahead-len(status.LocalOnlyCommits))
		}
		fmt.Fprintf(cfg.Stderr, "Reset %s from %s to %s; %d local-only commits leave this branch and will be retained under a recovery ref. Item branches are unchanged.\n", status.Repository.IntegrationBranch, status.LocalSHA, status.UpstreamSHA, status.Ahead)
	}
	result, err := cfg.Coordinator.SyncRepository(ctx, request)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, result)
	}
	writeRepositoryStatus(cfg, result.RepositoryStatusResult)
	if result.RecoveryRef != "" {
		fmt.Fprintf(cfg.Stdout, "Recovery ref: %s\n", result.RecoveryRef)
	}
	return nil
}
