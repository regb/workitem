package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/regb/workitem/internal/config"
	"path/filepath"
	"strings"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
)

const MethodRepositorySource = "repository_source"

type RepositorySourceRequest struct {
	Selector        string `json:"selector"`
	Revision        string `json:"revision,omitempty"`
	MetadataOnly    bool   `json:"metadata_only,omitempty"`
	SkipLabels      bool   `json:"skip_labels,omitempty"`
	ObserveUpstream bool   `json:"observe_upstream,omitempty"`
}
type RepositorySourceResult struct {
	Repository  RepositoryView `json:"repository"`
	GitDir      string         `json:"git_dir,omitempty"`
	Commit      string         `json:"commit"`
	PendingSync bool           `json:"pending_sync,omitempty"`
	Labels      []string       `json:"labels"`
}

func (d *Database) resolveRepository(r *model.Repository) error {
	if r.ID == "" {
		return nil
	}
	raw, err := d.ListProjection(RepositoryProjection)
	if err != nil {
		return err
	}
	b, ok := raw[r.ID]
	if !ok {
		return fmt.Errorf("repository %s is not registered", r.ID)
	}
	var repo model.OwnedRepository
	if err := json.Unmarshal(b, &repo); err != nil {
		return err
	}
	path, err := model.OwnedRepositoryGitDir(filepath.Dir(d.path), repo.ID)
	if err != nil {
		return err
	}
	r.Name = repo.Name
	r.IntegrationBranch = repo.IntegrationBranch
	r.RootAtCreation = path
	r.GitCommonDir = path
	r.CurrentRoot = ""
	r.RemoteURL = ""
	return nil
}
func (d *Database) RepositorySource(ctx context.Context, request RepositorySourceRequest) (RepositorySourceResult, error) {
	repos, err := d.ListRepositories()
	if err != nil {
		return RepositorySourceResult{}, err
	}
	for _, r := range repos {
		if r.ID != request.Selector && r.Name != request.Selector {
			continue
		}
		path, err := model.OwnedRepositoryGitDir(filepath.Dir(d.path), r.ID)
		if err != nil {
			return RepositorySourceResult{}, err
		}
		var pending RepositorySyncPlan
		pendingSync, err := d.ReadProjection(repositorySyncProjection, r.ID, &pending)
		if err != nil {
			return RepositorySourceResult{}, err
		}
		view := RepositoryView{OwnedRepository: r}
		if request.ObserveUpstream {
			view, err = d.repositoryView(ctx, r)
			if err != nil {
				return RepositorySourceResult{}, err
			}
		}
		if request.MetadataOnly {
			return RepositorySourceResult{Repository: view, GitDir: path, PendingSync: pendingSync}, nil
		}
		rev := request.Revision
		if _, relative := gitpkg.WorktreeRelativeRevision(rev); relative {
			return RepositorySourceResult{}, fmt.Errorf("base %q requires worktree resolution before querying the owned Git store", rev)
		}
		if rev == "^" {
			rev = ""
		}
		if rev == "" {
			rev = "refs/heads/" + r.IntegrationBranch
		}
		sha, err := gitpkg.New("git").RevParse(ctx, path, rev+"^{commit}")
		if err != nil {
			return RepositorySourceResult{}, err
		}
		labels := []string{}
		if !request.SkipLabels {
			if content, err := gitpkg.New("git").ReadBlob(ctx, path, sha, config.ProjectConfigRelativePath); err == nil {
				project, _, err := config.ReadProject(strings.NewReader(content))
				if err != nil {
					return RepositorySourceResult{}, err
				}
				labels = project.Item.Defaults.Labels
			}
		}
		return RepositorySourceResult{Repository: view, GitDir: path, Commit: sha, Labels: labels, PendingSync: pendingSync}, nil
	}
	return RepositorySourceResult{}, fmt.Errorf("unknown repository %q; onboard it with wi repo add NAME --from SOURCE", request.Selector)
}
func (c *Client) RepositorySource(ctx context.Context, selector, revision string) (RepositorySourceResult, error) {
	var result RepositorySourceResult
	err := c.Call(ctx, MethodRepositorySource, RepositorySourceRequest{Selector: selector, Revision: revision}, &result)
	return result, err
}
