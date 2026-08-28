package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/runtimepath"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestDeleteWorkItemsDeletesOnlyValidatedArchivedSet(t *testing.T) {
	st := store.New(t.TempDir())
	now := testutil.Time()
	archived := coreManifest(t, testutil.ID(t, 61), "archived", model.StateArchived, false, now)
	archived.Checkout = model.Checkout{Branch: model.ItemBranchName("item", archived.ID)}
	active := coreManifest(t, testutil.ID(t, 62), "active", model.StateBacklog, false, now)
	active.Checkout = model.Checkout{Branch: model.ItemBranchName("item", active.ID)}
	for _, manifest := range []model.Manifest{archived, active} {
		if err := st.CreateItem(context.Background(), manifest); err != nil {
			t.Fatal(err)
		}
	}
	application := New(st, nil)
	application.Process = nil
	application.AgentRuntimeStateRoot = filepath.Join(t.TempDir(), "state")
	application.AgentRuntimeSocketRoot = filepath.Join(t.TempDir(), "runtime")
	statePath := filepath.Join(application.AgentRuntimeStateRoot, "items", archived.ID, "runtimes", "old", "runtime.log")
	socketDir := filepath.Join(application.AgentRuntimeSocketRoot, filepath.FromSlash(runtimepath.ControlItemDir(archived.ID)))
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("diagnostic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := application.DeleteWorkItems(context.Background(), DeleteWorkItemsOptions{Archived: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != archived.ID {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(st.ItemDir(archived.ID)); !os.IsNotExist(err) {
		t.Fatalf("archived item still exists: %v", err)
	}
	if _, err := st.LoadManifest(active.ID); err != nil {
		t.Fatalf("active item removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(application.AgentRuntimeStateRoot, "items", archived.ID)); !os.IsNotExist(err) {
		t.Fatalf("runtime state remained after deletion: %v", err)
	}
	if _, err := os.Stat(socketDir); !os.IsNotExist(err) {
		t.Fatalf("runtime socket directory remained after deletion: %v", err)
	}
}

func TestDeleteWorkItemsValidatesAllBeforeDeletingAny(t *testing.T) {
	st := store.New(t.TempDir())
	now := testutil.Time()
	clean := coreManifest(t, testutil.ID(t, 63), "clean", model.StateArchived, false, now)
	clean.Checkout = model.Checkout{Branch: model.ItemBranchName("item", clean.ID)}
	unsafe := coreManifest(t, testutil.ID(t, 64), "unsafe", model.StateArchived, false, now)
	for _, manifest := range []model.Manifest{clean, unsafe} {
		if err := st.CreateItem(context.Background(), manifest); err != nil {
			t.Fatal(err)
		}
	}
	application := New(st, nil)
	application.Process = nil
	if _, err := application.DeleteWorkItems(context.Background(), DeleteWorkItemsOptions{Archived: true}); err == nil {
		t.Fatal("expected materialized checkout refusal")
	}
	if _, err := st.LoadManifest(clean.ID); err != nil {
		t.Fatalf("clean item deleted before validation completed: %v", err)
	}
}

func TestDeleteWorkItemsRejectsNonArchivedItem(t *testing.T) {
	st := store.New(t.TempDir())
	manifest := coreManifest(t, testutil.ID(t, 65), "active", model.StateBacklog, false, testutil.Time())
	manifest.Checkout = model.Checkout{Branch: model.ItemBranchName("item", manifest.ID)}
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	application := New(st, nil)
	application.Process = nil
	if _, err := application.DeleteWorkItems(context.Background(), DeleteWorkItemsOptions{ResolveOptions: ResolveOptions{Selector: manifest.ID}}); err == nil {
		t.Fatal("expected non-archived refusal")
	}
}
