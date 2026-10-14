package coordinator

import (
	"context"
	"fmt"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"path/filepath"
)

// RepositoryView combines registry identity with an optional live Git
// observation. It is never persisted as the repository record.
type RepositoryView struct {
	model.OwnedRepository
	Upstream       *model.RepositoryUpstream `json:"upstream,omitempty"`
	UpstreamSource string                    `json:"upstream_source,omitempty"`
	UpstreamError  string                    `json:"upstream_error,omitempty"`
}

func (d *Database) repositoryView(ctx context.Context, repo model.OwnedRepository) (RepositoryView, error) {
	path, err := model.OwnedRepositoryGitDir(filepath.Dir(d.path), repo.ID)
	if err != nil {
		return RepositoryView{}, err
	}
	up, err := gitpkg.New("git").BranchUpstream(ctx, path, repo.IntegrationBranch)
	view := RepositoryView{OwnedRepository: repo, Upstream: up, UpstreamSource: "git"}
	if err != nil {
		view.UpstreamError = err.Error()
	}
	return view, nil
}
func (c *Client) ListRepositoryViews(ctx context.Context) ([]RepositoryView, error) {
	var result []RepositoryView
	err := c.Call(ctx, MethodListRepositories, struct {
		ObserveUpstream bool `json:"observe_upstream"`
	}{true}, &result)
	if err == nil {
		for _, view := range result {
			if view.UpstreamSource != "git" {
				return nil, fmt.Errorf("daemon does not support Git-owned tracking; restart it before using repository commands")
			}
		}
	}
	return result, err
}
func (c *Client) requireGitTracking(ctx context.Context, selector string) error {
	var result RepositorySourceResult
	err := c.Call(ctx, MethodRepositorySource, RepositorySourceRequest{Selector: selector, MetadataOnly: true, ObserveUpstream: true}, &result)
	if err == nil && result.Repository.UpstreamSource != "git" {
		return fmt.Errorf("daemon does not support Git-owned tracking; restart it before using repository commands")
	}
	return err
}

func (c *Client) RepositoryViewSource(ctx context.Context, selector, revision string) (RepositorySourceResult, error) {
	var result RepositorySourceResult
	err := c.Call(ctx, MethodRepositorySource, RepositorySourceRequest{Selector: selector, Revision: revision, ObserveUpstream: true}, &result)
	if err == nil && result.Repository.UpstreamSource != "git" {
		return result, fmt.Errorf("daemon does not support Git-owned tracking; restart it before using repository commands")
	}
	return result, err
}
