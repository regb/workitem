package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	gitpkg "github.com/regb/workitem/internal/git"
)

type changedBranchAfterFetchGit struct {
	gitpkg.Client
	reads int
}

func (g *changedBranchAfterFetchGit) CurrentBranch(ctx context.Context, path string) (string, error) {
	g.reads++
	if g.reads > 1 {
		return "unexpected-branch", nil
	}
	return g.Client.CurrentBranch(ctx, path)
}

func TestMergeRevalidatesSourceAfterRemotePreflight(t *testing.T) {
	cfg, origin, _, _ := remoteCLI(t)
	ctx := context.Background()
	if err := runRepository(ctx, []string{"upstream", "project", origin}, cfg, false); err != nil {
		t.Fatal(err)
	}
	manifest, path := newRemoteMergeItem(t, cfg)
	old := remoteTestGit(t, path, "rev-parse", "HEAD")
	remote := remoteTestCommit(t, origin, "remote", "remote\n", "remote")
	cfg.App.Git = &changedBranchAfterFetchGit{Client: gitpkg.New("git")}
	result, err := cfg.App.MergeWorkItem(ctx, app.MergeOptions{Selector: manifest.ID, CWD: path})
	if err == nil || !strings.Contains(err.Error(), "source branch changed") || result.UpstreamSync == nil || !result.UpstreamSync.Changed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if remoteTestGit(t, path, "rev-parse", "HEAD") != old || remoteTestGit(t, path, "rev-parse", "main") != remote {
		t.Fatal("partial outcome not retained safely")
	}
}
