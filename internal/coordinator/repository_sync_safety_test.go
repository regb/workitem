package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

func TestRepositorySyncRejectsActiveIntegrationLock(t *testing.T) {
	f := newSyncFixture(t)
	old := f.status(false).LocalSHA
	guard, err := lock.Acquire(context.Background(), gitpkg.IntegrationLockPath(f.store))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	if _, err := f.sync("ff"); err == nil || !strings.Contains(err.Error(), "integration is active") {
		t.Fatalf("err=%v", err)
	}
	if _, err := f.server.repositoryStatus(context.Background(), RepositoryStatusRequest{Selector: "project", Fetch: true}); err == nil {
		t.Fatal("fetch did not refuse active integration")
	}
	if syncGit(t, f.store, "rev-parse", "main") != old {
		t.Fatal("integration changed")
	}
}

func TestRepositorySyncRejectsEscapingReconciliationParents(t *testing.T) {
	f := newSyncFixture(t)
	old := f.status(false).LocalSHA
	remote := f.commit(f.origin, "remote", "remote\n", "remote")
	f.status(true)
	id, _ := model.NewID()
	plan := RepositorySyncPlan{ID: id, Mode: "merge", Phase: "merging", OldSHA: old, UpstreamSHA: remote, RecoveryRef: "refs/wi/recovery/repository-sync/" + id}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(f.root, "reconciliation")); err != nil {
		t.Fatal(err)
	}
	if err := f.server.saveSyncPlan(importTestID, &plan); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sync("continue"); err == nil || !strings.Contains(err.Error(), "symlinks") {
		t.Fatalf("err=%v", err)
	}
	if files, err := os.ReadDir(outside); err != nil || len(files) != 0 {
		t.Fatal("external directory changed")
	}
	if syncGit(t, f.store, "rev-parse", "main") != old {
		t.Fatal("integration changed")
	}
}

func TestRepositorySyncRejectsMalformedRecoveryRef(t *testing.T) {
	f := newSyncFixture(t)
	old := f.status(false).LocalSHA
	remote := f.commit(f.origin, "remote", "remote\n", "remote")
	f.status(true)
	id, _ := model.NewID()
	plan := RepositorySyncPlan{ID: id, Mode: "reset", Phase: "prepared", OldSHA: old, UpstreamSHA: remote, NewSHA: remote, RecoveryRef: "refs/heads/unrelated"}
	if err := f.server.saveSyncPlan(importTestID, &plan); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sync("continue"); err == nil {
		t.Fatal("malformed recovery ref accepted")
	}
	if syncGit(t, f.store, "rev-parse", "main") != old {
		t.Fatal("integration changed")
	}
}

func TestRepositorySyncConflictingNativeRefMovementRetainsRecoveryState(t *testing.T) {
	f := newSyncFixture(t)
	old := f.localCommit("document", "local\n", "local")
	f.commit(f.origin, "document", "remote\n", "remote")
	if _, err := f.sync("merge"); err == nil {
		t.Fatal("expected conflict")
	}
	status := f.status(false)
	base := syncGit(t, f.store, "rev-parse", old+"^")
	syncGit(t, f.store, "update-ref", "refs/heads/main", base, old)
	if _, err := f.sync("continue"); err == nil || !strings.Contains(err.Error(), "tip changed") {
		t.Fatalf("err=%v", err)
	}
	if f.status(false).Pending == nil {
		t.Fatal("pending state discarded")
	}
	if _, err := os.Stat(status.ReconciliationPath); err != nil {
		t.Fatal("resolution checkout discarded")
	}
	if _, err := f.sync("abort"); err != nil {
		t.Fatal(err)
	}
	if syncGit(t, f.store, "rev-parse", "main") != base || f.status(false).Pending != nil {
		t.Fatal("abort altered externally moved main or retained reservation")
	}
}

func TestRepositorySyncFetchFailureDoesNotStartReconciliation(t *testing.T) {
	f := newSyncFixture(t)
	old := f.status(false).LocalSHA
	if err := os.Rename(f.origin, filepath.Join(t.TempDir(), "moved")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sync("ff"); err == nil {
		t.Fatal("unavailable remote accepted")
	}
	status := f.status(false)
	if status.LocalSHA != old || status.Pending != nil {
		t.Fatalf("status=%+v", status)
	}
}
