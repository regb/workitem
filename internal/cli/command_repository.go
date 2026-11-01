package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func writeRepositoryList(output io.Writer, repos []coordinator.RepositoryView) error {
	if len(repos) == 0 {
		return nil
	}
	writer := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tINTEGRATION\tSTATE\tUPSTREAM"); err != nil {
		return err
	}
	for _, repo := range repos {
		state := "active"
		if repo.Archived {
			state = "archived"
		}
		upstream := "untracked"
		if repo.Upstream != nil {
			upstream = repo.Upstream.String()
		}
		if repo.UpstreamError != "" {
			state = "problem"
			upstream = repo.UpstreamError
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", repo.Name, repo.IntegrationBranch, state, upstream); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func runRepository(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if cfg.Coordinator == nil {
		return fmt.Errorf("repository commands require the coordinator")
	}
	if len(args) == 0 {
		return usageErr{fmt.Errorf("usage: wi repo add|list|show|rename|archive|restore|upstream|status|sync|settings|files|env")}
	}
	switch args[0] {
	case "settings":
		return runRepositorySettings(ctx, args[1:], cfg, jsonOut)
	case "files":
		return runRepositoryFiles(ctx, args[1:], cfg, jsonOut)
	case "env":
		return runRepositoryEnv(ctx, args[1:], cfg, jsonOut)
	case "upstream", "status", "sync":
		return runRepositoryRemote(ctx, args, cfg, jsonOut)
	case "list":
		fs := newFlagSet("repo list", cfg.Stderr)
		all := fs.Bool("all", false, "include archived repositories")
		if err := fs.Parse(args[1:]); err != nil {
			return usageErr{err}
		}
		if fs.NArg() != 0 {
			return usageErr{fmt.Errorf("usage: wi repo list [--all]")}
		}
		repos, err := cfg.Coordinator.ListRepositoryViews(ctx)
		if err != nil {
			return err
		}
		if !*all {
			active := make([]coordinator.RepositoryView, 0, len(repos))
			for _, repo := range repos {
				if !repo.Archived {
					active = append(active, repo)
				}
			}
			repos = active
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, repos)
		}
		return writeRepositoryList(cfg.Stdout, repos)
	case "show":
		if len(args) > 2 {
			return usageErr{fmt.Errorf("usage: wi repo show [NAME]")}
		}
		explicit := ""
		if len(args) == 2 {
			explicit = args[1]
		}
		name, err := repositorySelector(ctx, cfg, args[0], explicit)
		if err != nil {
			return err
		}
		result, err := cfg.Coordinator.RepositoryViewSource(ctx, name, "")
		if err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		fmt.Fprintf(cfg.Stdout, "%s\nID: %s\nGit store: %s\nIntegration branch: %s\nCommit: %s\n", result.Repository.Name, result.Repository.ID, result.GitDir, result.Repository.IntegrationBranch, result.Commit)
		if result.Repository.UpstreamError != "" {
			fmt.Fprintf(cfg.Stdout, "Upstream configuration problem: %s\n", result.Repository.UpstreamError)
		} else if result.Repository.Upstream != nil {
			fmt.Fprintf(cfg.Stdout, "Upstream: %s\n", result.Repository.Upstream.String())
		} else {
			fmt.Fprintln(cfg.Stdout, "Upstream: none (local-only)")
		}
		if result.Repository.Archived {
			fmt.Fprintln(cfg.Stdout, "Archived: yes")
		}
		return nil
	case "archive", "restore":
		if len(args) > 2 {
			return usageErr{fmt.Errorf("usage: wi repo %s [NAME]", args[0])}
		}
		explicit := ""
		if len(args) == 2 {
			explicit = args[1]
		}
		name, err := repositorySelector(ctx, cfg, args[0], explicit)
		if err != nil {
			return err
		}
		repos, err := cfg.Coordinator.ListRepositories(ctx)
		if err != nil {
			return err
		}
		for _, repo := range repos {
			if repo.Name != name && repo.ID != name {
				continue
			}
			id, err := model.NewID()
			if err != nil {
				return err
			}
			result, err := cfg.Coordinator.SetRepositoryArchived(ctx, coordinator.SetRepositoryArchivedCommand{ID: id, ProtocolVersion: coordinator.ProtocolVersion, RepositoryID: repo.ID, PreviousArchived: repo.Archived, Archived: args[0] == "archive", CreatedAt: time.Now().UTC()})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cfg.Stdout, result)
			}
			state := "restored"
			if result.Repository.Archived {
				state = "archived"
			}
			if result.Changed {
				fmt.Fprintf(cfg.Stdout, "%s repository %s\n", state, result.Repository.Name)
			} else {
				fmt.Fprintf(cfg.Stdout, "repository %s is already %s\n", result.Repository.Name, state)
			}
			return nil
		}
		return fmt.Errorf("repository %q is not registered", name)
	case "rename":
		if len(args) < 2 || len(args) > 3 {
			return usageErr{fmt.Errorf("usage: wi repo rename [OLD] NEW")}
		}
		newName := args[len(args)-1]
		if !model.ValidRepositoryName(newName) {
			return usageErr{fmt.Errorf("invalid repository name %q", newName)}
		}
		explicit := ""
		if len(args) == 3 {
			explicit = args[1]
		}
		name, err := repositorySelector(ctx, cfg, args[0], explicit)
		if err != nil {
			return err
		}
		repos, err := cfg.Coordinator.ListRepositories(ctx)
		if err != nil {
			return err
		}
		for _, repo := range repos {
			if repo.Name != name && repo.ID != name {
				continue
			}
			id, err := model.NewID()
			if err != nil {
				return err
			}
			result, err := cfg.Coordinator.RenameRepository(ctx, coordinator.RenameRepositoryCommand{ID: id, ProtocolVersion: coordinator.ProtocolVersion, RepositoryID: repo.ID, PreviousName: repo.Name, Name: newName, CreatedAt: time.Now().UTC()})
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cfg.Stdout, result)
			}
			if result.Changed {
				fmt.Fprintf(cfg.Stdout, "renamed repository %s to %s\n", result.PreviousName, result.Repository.Name)
			} else {
				fmt.Fprintf(cfg.Stdout, "repository is already named %s\n", result.Repository.Name)
			}
			return nil
		}
		return fmt.Errorf("repository %q is not registered", name)
	case "add":
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			name := args[1]
			args = append(append([]string{"add"}, args[2:]...), name)
		}
		fs := newFlagSet("repo add", cfg.Stderr)
		var source, branch, upstreamValue, upstreamBranch string
		fs.StringVar(&upstreamValue, "upstream", "", "override the inferred integration upstream URL")
		fs.StringVar(&upstreamBranch, "upstream-branch", "", "remote branch; defaults to the integration branch")
		local := fs.Bool("local", false, "onboard without an upstream")
		fs.StringVar(&source, "from", "", "local checkout or Git URL to import")
		fs.StringVar(&branch, "branch", "", "integration branch; discovered for local sources")
		if err := fs.Parse(args[1:]); err != nil {
			return usageErr{err}
		}
		if fs.NArg() != 1 || source == "" {
			return usageErr{fmt.Errorf("usage: wi repo add --from SOURCE [--branch BRANCH] NAME")}
		}
		name := fs.Arg(0)
		if *local && (upstreamValue != "" || upstreamBranch != "") {
			return usageErr{fmt.Errorf("--local conflicts with --upstream and --upstream-branch")}
		}
		added, err := onboardRepository(ctx, cfg, repositoryOnboarding{Name: name, Source: source, Branch: branch, Upstream: upstreamValue, UpstreamBranch: upstreamBranch, Local: *local})
		if err != nil {
			return err
		}
		record, path, upstream, result := added.Record, added.GitDir, added.Upstream, added.Result
		if jsonOut {
			view, err := cfg.Coordinator.RepositoryViewSource(ctx, record.ID, "")
			if err != nil {
				return err
			}
			return writeJSON(cfg.Stdout, struct {
				Repository coordinator.RepositoryView `json:"repository"`
				Sequence   uint64                     `json:"sequence"`
				Duplicate  bool                       `json:"duplicate,omitempty"`
			}{view.Repository, result.Sequence, result.Duplicate})
		}
		fmt.Fprintf(cfg.Stdout, "added repository %s (%s)\nGit store: %s\nIntegration branch: %s\n", record.Name, record.ID, path, record.IntegrationBranch)
		if upstream != nil {
			fmt.Fprintf(cfg.Stdout, "Upstream: %s\n", upstream.String())
		}
		fmt.Fprintln(cfg.Stdout, "Project-local agent resources are approved in its managed worktrees; .envrc approval remains separate.")
		return nil
	default:
		return usageErr{fmt.Errorf("usage: wi repo add|list|show|rename|archive|restore|upstream|status|sync|settings|files|env")}
	}
}
