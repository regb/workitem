package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

func writeRepositoryList(output io.Writer, repos []model.OwnedRepository) error {
	writer := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	for _, repo := range repos {
		marker := ""
		if repo.Archived {
			marker = "\tarchived"
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s%s\n", repo.Name, repo.IntegrationBranch, marker); err != nil {
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
		return usageErr{fmt.Errorf("usage: wi repo add|list|show|rename|archive|restore")}
	}
	switch args[0] {
	case "list":
		fs := newFlagSet("repo list", cfg.Stderr)
		all := fs.Bool("all", false, "include archived repositories")
		if err := fs.Parse(args[1:]); err != nil {
			return usageErr{err}
		}
		if fs.NArg() != 0 {
			return usageErr{fmt.Errorf("usage: wi repo list [--all]")}
		}
		repos, err := cfg.Coordinator.ListRepositories(ctx)
		if err != nil {
			return err
		}
		if !*all {
			active := make([]model.OwnedRepository, 0, len(repos))
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
		if len(args) != 2 {
			return usageErr{fmt.Errorf("usage: wi repo show NAME")}
		}
		result, err := cfg.Coordinator.RepositorySource(ctx, args[1], "")
		if err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		fmt.Fprintf(cfg.Stdout, "%s\nID: %s\nGit store: %s\nIntegration branch: %s\nCommit: %s\n", result.Repository.Name, result.Repository.ID, result.GitDir, result.Repository.IntegrationBranch, result.Commit)
		if result.Repository.Archived {
			fmt.Fprintln(cfg.Stdout, "Archived: yes")
		}
		return nil
	case "archive", "restore":
		if len(args) != 2 {
			return usageErr{fmt.Errorf("usage: wi repo %s NAME", args[0])}
		}
		repos, err := cfg.Coordinator.ListRepositories(ctx)
		if err != nil {
			return err
		}
		for _, repo := range repos {
			if repo.Name != args[1] && repo.ID != args[1] {
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
		return fmt.Errorf("repository %q is not registered", args[1])
	case "rename":
		if len(args) != 3 {
			return usageErr{fmt.Errorf("usage: wi repo rename OLD NEW")}
		}
		if !model.ValidRepositoryName(args[2]) {
			return usageErr{fmt.Errorf("invalid repository name %q", args[2])}
		}
		repos, err := cfg.Coordinator.ListRepositories(ctx)
		if err != nil {
			return err
		}
		for _, repo := range repos {
			if repo.Name != args[1] {
				continue
			}
			id, err := model.NewID()
			if err != nil {
				return err
			}
			result, err := cfg.Coordinator.RenameRepository(ctx, coordinator.RenameRepositoryCommand{ID: id, ProtocolVersion: coordinator.ProtocolVersion, RepositoryID: repo.ID, PreviousName: repo.Name, Name: args[2], CreatedAt: time.Now().UTC()})
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
		return fmt.Errorf("repository %q is not registered", args[1])
	case "add":
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			name := args[1]
			args = append(append([]string{"add"}, args[2:]...), name)
		}
		fs := newFlagSet("repo add", cfg.Stderr)
		var source, branch string
		fs.StringVar(&source, "from", "", "local checkout or Git URL to import")
		fs.StringVar(&branch, "branch", "", "integration branch; discovered for local sources")
		if err := fs.Parse(args[1:]); err != nil {
			return usageErr{err}
		}
		if fs.NArg() != 1 || source == "" {
			return usageErr{fmt.Errorf("usage: wi repo add --from SOURCE [--branch BRANCH] NAME")}
		}
		name := fs.Arg(0)
		if !strings.Contains(source, ":") {
			if !filepath.IsAbs(source) {
				source = filepath.Join(cfg.CWD, source)
			}
		}
		client := gitpkg.New("git")
		origin := ""
		if strings.Contains(source, ":") {
			origin = source
			if branch == "" {
				return fmt.Errorf("choose --branch for URL onboarding")
			}
		} else {
			info, err := client.DetectRepository(ctx, source, "")
			if err != nil {
				return err
			}
			origin = info.Repository.RemoteURL
			if branch == "" {
				branch, err = client.DefaultBranch(ctx, source)
				if err != nil {
					return err
				}
			}
		}
		status, err := cfg.Coordinator.Status(ctx)
		if err != nil {
			return err
		}
		guard, err := lock.Acquire(ctx, filepath.Join(status.DataRoot, "locks", "repository-onboarding.lock"))
		if err != nil {
			return err
		}
		defer guard.Release()
		repos, err := cfg.Coordinator.ListRepositories(ctx)
		if err != nil {
			return err
		}
		for _, r := range repos {
			if r.Name == name {
				return fmt.Errorf("repository name %s already exists", name)
			}
		}
		id, err := model.NewID()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		record, err := model.NormalizeOwnedRepository(model.OwnedRepository{ID: id, Name: name, IntegrationBranch: branch, OriginURL: origin, CreatedAt: now})
		if err != nil {
			return err
		}
		path, err := model.OwnedRepositoryGitDir(status.DataRoot, id)
		if err != nil {
			return err
		}
		if err := client.ImportBare(ctx, model.BareImportOptions{Source: source, Destination: path, IntegrationBranch: branch, OriginURL: record.OriginURL}); err != nil {
			return err
		}
		commandID, err := model.NewID()
		if err != nil {
			return err
		}
		result, err := cfg.Coordinator.RegisterRepository(ctx, coordinator.RegisterRepositoryCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Repository: record, Source: source, CreatedAt: now})
		if err != nil {
			return fmt.Errorf("imported Git store retained at %s, but registration failed: %w", path, err)
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, result)
		}
		fmt.Fprintf(cfg.Stdout, "added repository %s (%s)\nGit store: %s\nIntegration branch: %s\n", record.Name, record.ID, path, branch)
		fmt.Fprintln(cfg.Stdout, "Project-local agent resources are approved in its managed worktrees; .envrc approval remains separate.")
		return nil
	default:
		return usageErr{fmt.Errorf("usage: wi repo add|list|show|rename|archive|restore")}
	}
}
