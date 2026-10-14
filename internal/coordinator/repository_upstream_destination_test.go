package coordinator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func TestRepositoryUpstreamRequiresItsGitRemote(t *testing.T) {
	f := newSyncFixture(t)
	f.status(true)
	remote := f.commit(f.origin, "remote", "remote\n", "remote")
	syncGit(t, f.store, "remote", "remove", "origin")
	result, err := f.sync("ff")
	if err == nil || result.LocalSHA == remote {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRepositoryUpstreamChangesDoNotReuseOtherDestinationObservations(t *testing.T) {
	for _, change := range []string{"url", "branch"} {
		t.Run(change, func(t *testing.T) {
			f := newSyncFixture(t)
			old := f.status(true)
			if old.FetchedAt == nil {
				t.Fatal("missing fetch observation")
			}
			next := *old.Repository.Upstream
			if change == "url" {
				next.URL = filepath.Join(t.TempDir(), "another.git")
			} else {
				next.Branch = "other-branch"
			}
			next.TrackingRef = "refs/remotes/" + next.Remote + "/" + next.Branch
			id, _ := model.NewID()
			if _, err := f.server.setRepositoryUpstream(context.Background(), SetRepositoryUpstreamCommand{ID: id, ProtocolVersion: ProtocolVersion, RepositoryID: importTestID, PreviousUpstream: old.Repository.Upstream, Upstream: &next, CreatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			status, err := f.server.repositoryStatus(context.Background(), RepositoryStatusRequest{Selector: "project"})
			if err != nil || status.State != "unfetched" || status.FetchedAt != nil || status.UpstreamSHA != "" {
				t.Fatalf("status=%+v err=%v", status, err)
			}
		})
	}
}
