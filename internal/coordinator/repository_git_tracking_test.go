package coordinator

import (
	"context"
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
