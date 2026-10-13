package coordinator

import (
	"context"
	"github.com/regb/workitem/internal/model"
	"testing"
)

func TestRepositorySyncAppliedJournalDoesNotOverwriteLaterNativeAdvance(t *testing.T) {
	f := newSyncFixture(t)
	old := f.status(false).LocalSHA
	remote := f.commit(f.origin, "remote", "remote\n", "remote")
	f.status(true)
	id, _ := model.NewID()
	plan := RepositorySyncPlan{ID: id, Mode: "ff", Phase: "applied", OldSHA: old, UpstreamSHA: remote, NewSHA: remote, RecoveryRef: "refs/wi/recovery/repository-sync/" + id}
	syncGit(t, f.store, "update-ref", "refs/heads/main", remote, old)
	if err := f.server.saveSyncPlan(importTestID, &plan); err != nil {
		t.Fatal(err)
	}
	later := f.localCommit("later", "later\n", "later native advance")
	result, err := f.server.repositorySync(context.Background(), RepositorySyncRequest{Selector: "project", Mode: "continue"})
	if err != nil || result.Pending != nil || result.LocalSHA != later {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if syncGit(t, f.store, "rev-parse", plan.RecoveryRef) != old {
		t.Fatal("recovery missing")
	}
}
