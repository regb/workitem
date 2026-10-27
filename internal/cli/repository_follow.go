package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
)

// followUpstreamTimeout bounds the fetch that creation waits for.
const followUpstreamTimeout = 20 * time.Second

// followUpstream brings a repository's integration branch up to date with
// its upstream before a new item records its base, when [repo]
// follow_upstream is fast-forward. It synchronizes like wi repo sync without
// options: a behind branch fast-forwards, an ahead one is left alone, and
// divergence is refused. It never blocks creation: a repository without an
// upstream is skipped, and a failure becomes a warning, the item starting
// from the local integration branch.
func followUpstream(ctx context.Context, cfg Config, repositoryID string) []string {
	if cfg.App.RepoConfig.FollowUpstream != config.FollowUpstreamFastForward || cfg.Coordinator == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, followUpstreamTimeout)
	defer cancel()
	var source coordinator.RepositorySourceResult
	if err := cfg.Coordinator.Call(ctx, coordinator.MethodRepositorySource, coordinator.RepositorySourceRequest{Selector: repositoryID, MetadataOnly: true, ObserveUpstream: true}, &source); err != nil {
		return []string{fmt.Sprintf("could not follow the upstream: %v; the item starts from the local integration branch", err)}
	}
	if source.Repository.Upstream == nil || source.Repository.UpstreamSource != "git" {
		return nil
	}
	if _, err := cfg.Coordinator.SyncRepository(ctx, coordinator.RepositorySyncRequest{Selector: source.Repository.ID}); err != nil {
		return []string{fmt.Sprintf("could not follow the upstream: %v; the item starts from the local %s", err, source.Repository.IntegrationBranch)}
	}
	return nil
}

// printWarnings reports warnings of a command that failed later, so they
// are not lost with its result.
func printWarnings(cfg Config, warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintf(cfg.Stderr, "warning: %s\n", warning)
	}
}
