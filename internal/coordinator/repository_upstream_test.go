package coordinator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func TestRepositoryTrackingIsReadFromGitNotStoredInRegistry(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	previous := f.status(false).Repository.Upstream
	next, _ := model.ParseRepositoryUpstream(f.origin, "release")
	if _, err := f.server.setRepositoryUpstream(ctx, SetRepositoryUpstreamCommand{ID: "native-tracking", ProtocolVersion: ProtocolVersion, RepositoryID: importTestID, PreviousUpstream: previous, Upstream: next, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	raw, err := f.server.database.ListProjection(RepositoryProjection)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw[importTestID]), "upstream") {
		t.Fatal("Git tracking duplicated in registry")
	}
	syncGit(t, f.store, "remote", "set-url", "origin", "https://new.example.com/project.git")
	if f.status(false).Repository.Upstream.URL != "https://new.example.com/project.git" {
		t.Fatal("native config edit ignored")
	}
	syncGit(t, f.store, "config", "--unset", "branch.main.remote")
	syncGit(t, f.store, "config", "--unset", "branch.main.merge")
	if f.status(false).State != "untracked" {
		t.Fatal("absent native tracking is not local-only")
	}
}

func TestGitTrackingEditInterruptedBeforeAcknowledgementCanBeRetried(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	previous := f.status(false).Repository.Upstream
	next, _ := model.ParseRepositoryUpstream(f.origin, "other")
	// Simulate a completed atomic native edit whose DB acknowledgement never ran.
	syncGit(t, f.store, "config", "branch.main.merge", "refs/heads/other")
	command := SetRepositoryUpstreamCommand{ID: "interrupted-edit", ProtocolVersion: ProtocolVersion, RepositoryID: importTestID, PreviousUpstream: previous, Upstream: next, CreatedAt: time.Now().UTC()}
	result, err := f.server.setRepositoryUpstream(ctx, command)
	if err != nil || !result.Changed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	// A later Git edit is authoritative. Replaying an acknowledged command must
	// not reapply its historical configuration.
	syncGit(t, f.store, "config", "branch.main.merge", "refs/heads/later")
	duplicate, err := f.server.setRepositoryUpstream(ctx, command)
	if err != nil || !duplicate.Duplicate || f.status(false).Repository.Upstream.Branch != "later" {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
}

func TestRepositoryUpstreamDurableCASAndValidation(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	previous := f.status(false).Repository.Upstream
	target, err := model.ParseRepositoryUpstream(f.origin, "release/stable")
	if err != nil {
		t.Fatal(err)
	}
	command := SetRepositoryUpstreamCommand{ID: "upstream-1", ProtocolVersion: ProtocolVersion, RepositoryID: importTestID, PreviousUpstream: previous, Upstream: target, CreatedAt: time.Now().UTC()}
	first, err := f.server.setRepositoryUpstream(ctx, command)
	if err != nil || !first.Changed {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := f.server.setRepositoryUpstream(ctx, command)
	if err != nil || !second.Duplicate {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	stale := command
	stale.ID = "stale"
	stale.Upstream = nil
	if _, err := f.server.setRepositoryUpstream(ctx, stale); err == nil {
		t.Fatal("stale change accepted")
	}
	noop := command
	noop.ID = "noop"
	noop.PreviousUpstream = target
	if result, err := f.server.setRepositoryUpstream(ctx, noop); err != nil || result.Changed {
		t.Fatalf("noop=%+v err=%v", result, err)
	}
	malformed := command
	malformed.ID = "bad"
	malformed.Upstream = &model.RepositoryUpstream{URL: "--upload-pack", Branch: "main"}
	if _, err := f.server.setRepositoryUpstream(ctx, malformed); err == nil {
		t.Fatal("invalid remote accepted")
	}
	f.server.Close()
	f.server, err = NewServer(f.root, f.socket)
	if err != nil {
		t.Fatal(err)
	}
	status := f.status(false)
	if status.Repository.Upstream == nil || status.Repository.Upstream.Branch != "release/stable" {
		t.Fatalf("status=%+v", status)
	}
	pending, _ := json.Marshal(RepositorySyncPlan{ID: importTestID})
	if err := f.server.database.UpdateProjections([]ProjectionUpdate{{Projection: repositorySyncProjection, Key: importTestID, Value: pending}}); err != nil {
		t.Fatal(err)
	}
	clear := command
	clear.ID = "clear"
	clear.PreviousUpstream = target
	clear.Upstream = nil
	if _, err := f.server.setRepositoryUpstream(ctx, clear); err == nil {
		t.Fatal("pending upstream cleared")
	}
}

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

func TestGitTrackingChangeDuringReconciliationBlocksContinueButAllowsAbort(t *testing.T) {
	f := newSyncFixture(t)
	f.localCommit("document", "local\n", "local")
	f.commit(f.origin, "document", "remote\n", "remote")
	if _, err := f.sync("merge"); err == nil {
		t.Fatal("expected conflicts")
	}
	status := f.status(false)
	syncGit(t, status.ReconciliationPath, "checkout", "--ours", "document")
	syncGit(t, status.ReconciliationPath, "add", "document")
	syncGit(t, f.store, "remote", "set-url", "origin", "https://different.example.com/project.git")
	if _, err := f.sync("continue"); err == nil || !strings.Contains(err.Error(), "upstream changed") {
		t.Fatalf("err=%v", err)
	}
	if _, err := f.sync("abort"); err != nil {
		t.Fatal(err)
	}
	if f.status(false).Pending != nil {
		t.Fatal("reservation survived abort")
	}
}

func TestPlanWithoutUpstreamSnapshotRetainsAbort(t *testing.T) {
	f := newSyncFixture(t)
	f.localCommit("document", "local\n", "local")
	f.commit(f.origin, "document", "remote\n", "remote")
	if _, err := f.sync("merge"); err == nil {
		t.Fatal("expected conflict")
	}
	status := f.status(false)
	status.Pending.Upstream = nil
	if err := f.server.saveSyncPlan(importTestID, status.Pending); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sync("continue"); err == nil || !strings.Contains(err.Error(), "missing upstream snapshot") {
		t.Fatalf("err=%v", err)
	}
	if _, err := f.sync("abort"); err != nil {
		t.Fatal(err)
	}
}
