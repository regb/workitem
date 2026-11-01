package coordinator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestProjectedWorktreeBranchPolicy(t *testing.T) {
	path := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	base := git("rev-parse", "HEAD")
	observer := NewExternalObserver(nil, t.TempDir())
	for _, detached := range []bool{false, true} {
		if detached {
			git("checkout", "--detach", base)
		} else {
			git("checkout", "main")
		}
		m := model.Manifest{Repository: model.Repository{CreatedFromCommit: base}, Checkout: model.Checkout{Specification: model.CheckoutSpecification{Kind: model.WorkspaceKindManagedSlot, Branch: "master"}, ClaimGeneration: 1, Claim: &model.WorkspaceClaim{ID: "test-claim", Generation: 1, Executor: model.ExecutorLocal, Path: path}}}
		got := observer.observeWorktree(context.Background(), m)
		if got.Status != "changed" || !got.BranchMismatch || !got.HasChanges {
			t.Fatalf("managed=%+v", got)
		}
	}
}

func TestWorktreeInspectionAppliesOnlyToTheSameCheckout(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	if out, err := exec.Command("git", "-C", path, "init", "-b", "wi/inspect").CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	checkout := model.Checkout{Specification: model.CheckoutSpecification{Kind: model.WorkspaceKindManagedSlot, Branch: "wi/inspect"}, ClaimGeneration: 1, Claim: &model.WorkspaceClaim{ID: "claim", Generation: 1, Executor: model.ExecutorLocal, Path: path}}
	manifest := model.NewManifest(importTestID, "inspect", "Inspect", nil, false, time.Now().UTC(), model.Repository{}, checkout)
	manifest.State = model.StateActive
	if err := native.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := seedManifests(context.Background(), db, root); err != nil {
		t.Fatal(err)
	}
	observer := NewExternalObserver(db, root)
	observed := func() *ProjectedWorktree {
		t.Helper()
		var observation AgentObservation
		if found, err := db.ReadProjection(AgentObservationProjection, importTestID, &observation); err != nil || !found {
			t.Fatalf("found=%v err=%v", found, err)
		}
		return observation.Worktree
	}
	inspection, err := observer.Inspect(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.ReconcileWith(context.Background(), inspection); err != nil {
		t.Fatal(err)
	}
	if got := observed(); got == nil || got.Status != "clean" || got.CheckoutPath != path {
		t.Fatalf("worktree=%+v", got)
	}
	// Observing again changes only timestamps: readers are not woken.
	before, _ := db.changes.since("", 0)
	if err := observer.ReconcileWith(context.Background(), inspection); err != nil {
		t.Fatal(err)
	}
	if after, _ := db.changes.since("", 0); after.Revision != before.Revision {
		t.Fatalf("an unchanged observation was recorded as a change: %d -> %d", before.Revision, after.Revision)
	}
	// A worktree that became dirty is a change readers learn of at once.
	if err := os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty, err := observer.Inspect(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.ReconcileWith(context.Background(), dirty); err != nil {
		t.Fatal(err)
	}
	if after, _ := db.changes.since(before.Epoch, before.Revision); len(after.ItemIDs) != 1 || after.ItemIDs[0] != importTestID {
		t.Fatalf("dirty worktree not recorded: %+v", after)
	}
	if err := os.Remove(filepath.Join(path, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	if err := observer.ReconcileWith(context.Background(), inspection); err != nil {
		t.Fatal(err)
	}
	// The item moved to another checkout while Git ran: the inspection no
	// longer applies, and the last observation stays.
	stale := inspection.checkouts[importTestID]
	stale.path = "/elsewhere"
	stale.worktree = &ProjectedWorktree{Status: "problem"}
	inspection.checkouts[importTestID] = stale
	if err := observer.ReconcileWith(context.Background(), inspection); err != nil {
		t.Fatal(err)
	}
	if got := observed(); got == nil || got.Status != "clean" {
		t.Fatalf("stale inspection applied: %+v", got)
	}
}

func TestAFailedSetupMakesTheCheckoutAProblem(t *testing.T) {
	path := t.TempDir()
	if out, err := exec.Command("git", "-C", path, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	if out, err := exec.Command("git", "-C", path, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	observer := NewExternalObserver(nil, t.TempDir())
	observer.setupRuns = store.SetupRuns{Dir: t.TempDir()}
	m := model.Manifest{ID: "item", Checkout: model.Checkout{Specification: model.CheckoutSpecification{Kind: model.WorkspaceKindManagedSlot, Branch: "main"}, ClaimGeneration: 1, Claim: &model.WorkspaceClaim{ID: "claim", Generation: 1, Executor: model.ExecutorLocal, Path: path}}}
	if got := observer.observeWorktree(context.Background(), m); got.Status == "problem" {
		t.Fatalf("no setup = %+v", got)
	}
	if err := observer.setupRuns.Write("item", store.SetupRun{ClaimID: "claim", Status: store.SetupFailed, Error: "command failed: exit status 1"}); err != nil {
		t.Fatal(err)
	}
	if got := observer.observeWorktree(context.Background(), m); got.Status != "problem" || !strings.Contains(got.Reason, "exit status 1") {
		t.Fatalf("failed setup = %+v", got)
	}
	// Only the current claim's setup counts.
	if err := observer.setupRuns.Write("item", store.SetupRun{ClaimID: "earlier", Status: store.SetupFailed}); err != nil {
		t.Fatal(err)
	}
	if got := observer.observeWorktree(context.Background(), m); got.Status == "problem" {
		t.Fatalf("earlier claim = %+v", got)
	}
}
