package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
)

// newFollowItem creates an item and returns its manifest and warnings.
func newFollowItem(t *testing.T, cfg Config) (model.Manifest, []string) {
	t.Helper()
	var out bytes.Buffer
	cfg.Stdout = &out
	if code := Run(context.Background(), []string{"new", "--repo", "project", "--title", "Feature", "--json"}, cfg); code != ExitOK {
		t.Fatalf("new code=%d stderr=%v", code, cfg.Stderr)
	}
	var result struct {
		Manifest model.Manifest `json:"manifest"`
		Warnings []string       `json:"warnings"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Manifest, result.Warnings
}

func TestNewFollowsUpstreamOnlyWhenConfigured(t *testing.T) {
	cfg, origin, _, _ := remoteCLI(t)
	ctx := context.Background()
	if err := runRepository(ctx, []string{"upstream", "project", origin}, cfg, false); err != nil {
		t.Fatal(err)
	}
	source, err := cfg.Coordinator.RepositoryViewSource(ctx, "project", "")
	if err != nil {
		t.Fatal(err)
	}
	old := source.Commit
	published := remoteTestCommit(t, origin, "remote", "remote\n", "published")

	cfg.App.RepoConfig.FollowUpstream = config.FollowUpstreamOff
	if manifest, warnings := newFollowItem(t, cfg); manifest.Repository.CreatedFromCommit != old || len(warnings) != 0 {
		t.Fatalf("off: created from %s, warnings %v", manifest.Repository.CreatedFromCommit, warnings)
	}
	cfg.App.RepoConfig.FollowUpstream = config.FollowUpstreamFastForward
	if manifest, warnings := newFollowItem(t, cfg); manifest.Repository.CreatedFromCommit != published || len(warnings) != 0 {
		t.Fatalf("fast-forward: created from %s, want %s; warnings %v", manifest.Repository.CreatedFromCommit, published, warnings)
	}

	// Divergence is refused, as wi repo sync does: the item still starts,
	// from the local branch, with a warning.
	local := remoteTestGit(t, source.GitDir, "commit-tree", "-p", published, "-m", "local", published+"^{tree}")
	remoteTestGit(t, source.GitDir, "update-ref", "refs/heads/main", local, published)
	remoteTestCommit(t, origin, "other", "other\n", "other")
	manifest, warnings := newFollowItem(t, cfg)
	if manifest.Repository.CreatedFromCommit != local || len(warnings) != 1 || !strings.Contains(warnings[0], "diverged") {
		t.Fatalf("diverged: created from %s, want %s; warnings %v", manifest.Repository.CreatedFromCommit, local, warnings)
	}
}
