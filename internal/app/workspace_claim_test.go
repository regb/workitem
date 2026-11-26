package app_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

// A checkout is claimed without a terminal or a conversation, reports branch
// drift, is released only when clean and without a terminal, and a later
// claim reuses the branch and slot under a new claim generation. A closed
// item cannot claim one.
func TestWorkspaceClaimAndReleaseInvariants(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := newTestApp(st, fg)
	application.Tmux = ft
	application.Clock = fixedClock{testutil.Time()}
	item := createItem(t, application, app.NewWorkItemOptions{Title: "Checkout"})
	opts := app.ResolveOptions{Selector: item.ID}

	claimed, err := application.EnsureWorkItemWorkspace(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	initial := claimed.Checkout
	if !initial.Present() || initial.Claim == nil || initial.Specification.Kind != model.WorkspaceKindManagedSlot {
		t.Fatalf("claimed checkout = %+v", initial)
	}
	if len(ft.ensured) != 0 || claimed.Manifest.PrimaryConversation != nil {
		t.Fatalf("workspace ensure crossed boundaries: manifest=%+v tmux=%+v", claimed.Manifest, ft.ensured)
	}

	fg.currentBranch = "main"
	status, err := application.WorkspaceStatus(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Git.BranchMismatch || status.Git.CurrentBranch != "main" || status.Git.ExpectedBranch != item.Checkout.Specification.Branch {
		t.Fatalf("branch status = %+v", status)
	}
	fg.currentBranch = ""

	fg.status = " M file.go\n"
	if _, err := application.ReleaseWorkItemWorkspace(ctx, opts); err == nil {
		t.Fatal("released a dirty checkout")
	}
	fg.status = ""

	ft.sessions = map[string]bool{item.TerminalSessionName(): true}
	if _, err := application.ReleaseWorkItemWorkspace(ctx, opts); err == nil || !strings.Contains(err.Error(), "terminal session") {
		t.Fatalf("release should require an explicit terminal close: %v", err)
	}
	if _, err := application.CloseTerminal(ctx, opts); err != nil {
		t.Fatal(err)
	}

	released, err := application.ReleaseWorkItemWorkspace(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !released.Changed || released.Manifest.State == model.StateClosed || released.Manifest.Checkout.Present() || released.Manifest.Checkout.Claim != nil {
		t.Fatalf("released = %+v", released)
	}
	if released.Manifest.Checkout.ClaimGeneration != initial.Claim.Generation || released.Manifest.Checkout.Specification != initial.Specification {
		t.Fatalf("release lost checkout intent or generation: %+v", released.Manifest.Checkout)
	}
	if _, err := os.Stat(initial.Claim.Path); err != nil {
		t.Fatalf("slot should remain after release: %v", err)
	}

	reclaimed, err := application.EnsureWorkItemWorkspace(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	claim := reclaimed.Checkout.Claim
	if claim == nil || claim.Path != initial.Claim.Path || claim.ID == initial.Claim.ID || claim.Generation != initial.Claim.Generation+1 || reclaimed.Checkout.Specification.Branch != initial.Specification.Branch {
		t.Fatalf("reclaim reused the claim identity or changed the branch: old=%+v new=%+v", initial, reclaimed.Checkout)
	}

	closed := createItem(t, application, app.NewWorkItemOptions{Title: "Closed"})
	if _, err := application.Close(ctx, app.ResolveOptions{Selector: closed.ID}, app.CloseOptions{Resolution: model.ResolutionCompleted}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.EnsureWorkItemWorkspace(ctx, app.ResolveOptions{Selector: closed.ID}); err == nil || !strings.Contains(err.Error(), "is closed") {
		t.Fatalf("a closed item claimed a checkout: %v", err)
	}
}
