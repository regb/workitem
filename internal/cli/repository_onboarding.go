package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

// repositoryOnboarding is what wi repo add and the web form ask for: a
// local checkout or Git URL to import under a new name.
type repositoryOnboarding struct {
	Name, Source, Branch     string
	Upstream, UpstreamBranch string
	Local                    bool
}

type repositoryOnboarded struct {
	Record   model.OwnedRepository
	GitDir   string
	Upstream *model.RepositoryUpstream
	Result   coordinator.RegisterRepositoryResult
}

// onboardRepository imports a repository into a new owned bare store and
// registers it. A URL source without a branch uses the remote's default.
func onboardRepository(ctx context.Context, cfg Config, opts repositoryOnboarding) (repositoryOnboarded, error) {
	name, source, branch, upstreamBranch := opts.Name, opts.Source, opts.Branch, opts.UpstreamBranch
	var upstream *model.RepositoryUpstream
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
			discovered, err := remoteDefaultBranch(ctx, source)
			if err != nil {
				return repositoryOnboarded{}, fmt.Errorf("choose the integration branch: %w", err)
			}
			branch = discovered
		}
	} else {
		info, err := client.DetectRepository(ctx, source, "")
		if err != nil {
			return repositoryOnboarded{}, err
		}
		origin = info.Repository.RemoteURL
		if branch == "" {
			branch, err = client.DefaultBranch(ctx, source)
			if err != nil {
				return repositoryOnboarded{}, err
			}
		}
	}
	if !opts.Local {
		if opts.Upstream != "" {
			origin = opts.Upstream
		}
		if origin != "" {
			if !strings.Contains(origin, ":") && !filepath.IsAbs(origin) {
				origin = filepath.Join(source, origin)
			}
			if upstreamBranch == "" {
				upstreamBranch = branch
				if opts.Upstream == "" && !strings.Contains(source, ":") {
					tracked, err := client.OriginTrackingBranch(ctx, source, branch)
					if err != nil {
						return repositoryOnboarded{}, err
					}
					if tracked != "" {
						upstreamBranch = tracked
					}
				}
			}
			var err error
			upstream, err = model.ParseRepositoryUpstream(origin, upstreamBranch)
			if err != nil {
				return repositoryOnboarded{}, usageErr{err}
			}
		} else if upstreamBranch != "" {
			return repositoryOnboarded{}, usageErr{fmt.Errorf("--upstream-branch requires an inferred or explicit upstream URL")}
		}
	}
	status, err := cfg.Coordinator.Status(ctx)
	if err != nil {
		return repositoryOnboarded{}, err
	}
	guard, err := lock.Acquire(ctx, filepath.Join(status.DataRoot, "locks", "repository-onboarding.lock"))
	if err != nil {
		return repositoryOnboarded{}, err
	}
	defer guard.Release()
	repos, err := cfg.Coordinator.ListRepositories(ctx)
	if err != nil {
		return repositoryOnboarded{}, err
	}
	for _, r := range repos {
		if r.Name == name {
			return repositoryOnboarded{}, fmt.Errorf("repository name %s already exists", name)
		}
	}
	id, err := model.NewID()
	if err != nil {
		return repositoryOnboarded{}, err
	}
	now := time.Now().UTC()
	record, err := model.NormalizeOwnedRepository(model.OwnedRepository{ID: id, Name: name, IntegrationBranch: branch, CreatedAt: now})
	if err != nil {
		return repositoryOnboarded{}, err
	}
	path, err := model.OwnedRepositoryGitDir(status.DataRoot, id)
	if err != nil {
		return repositoryOnboarded{}, err
	}
	remoteURL := origin
	upstreamBranch = ""
	if remoteURL != "" && !strings.Contains(remoteURL, ":") && !filepath.IsAbs(remoteURL) {
		remoteURL = filepath.Join(source, remoteURL)
	}
	if upstream != nil {
		remoteURL = upstream.URL
		upstreamBranch = upstream.Branch
	}
	if err := client.ImportBare(ctx, model.BareImportOptions{Source: source, Destination: path, IntegrationBranch: branch, OriginURL: remoteURL, UpstreamBranch: upstreamBranch}); err != nil {
		return repositoryOnboarded{}, err
	}
	commandID, err := model.NewID()
	if err != nil {
		return repositoryOnboarded{}, err
	}
	result, err := cfg.Coordinator.RegisterRepository(ctx, coordinator.RegisterRepositoryCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Repository: record, CreatedAt: now})
	if err != nil {
		return repositoryOnboarded{}, fmt.Errorf("imported Git store retained at %s, but registration failed: %w", path, err)
	}
	return repositoryOnboarded{Record: record, GitDir: path, Upstream: upstream, Result: result}, nil
}

// remoteDefaultBranch asks a remote which branch its HEAD names.
func remoteDefaultBranch(ctx context.Context, url string) (string, error) {
	output, err := exec.CommandContext(ctx, "git", "ls-remote", "--symref", url, "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("read the default branch of %s: %w", url, err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if ref, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			if branch, _, found := strings.Cut(ref, "\t"); found {
				return branch, nil
			}
		}
	}
	return "", errors.New("the remote names no default branch")
}
