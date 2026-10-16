package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestNamingPolicyKeepsActiveSlugAndBranchButAllowsTitles(t *testing.T) {
	for _, state := range []string{model.StateBacklog, model.StateActive, model.StateClosed} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			native := store.New(root)
			if err := native.Ensure(); err != nil {
				t.Fatal(err)
			}
			m := model.NewManifest(importTestID, "task", "Original", nil, false, time.Now(), model.Repository{}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "wi/task-original"))
			m.State = state
			if state == model.StateClosed {
				m.Slug = ""
			}
			m.Checkout.ClaimGeneration = 1
			if err := native.CreateItem(context.Background(), m); err != nil {
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
			before, _ := db.CanonicalManifest(m.ID)
			result, err := db.ExecuteManifestCommand(ManifestCommand{ID: "title", ProtocolVersion: ProtocolVersion, Type: CommandTitleSet, ItemID: m.ID, ExpectedRevision: &before.Revision, Title: "Clarified title", CreatedAt: time.Now()})
			if err != nil || result.Manifest.Title != "Clarified title" || result.Manifest.Slug != m.Slug || !reflect.DeepEqual(result.Manifest.Checkout, m.Checkout) {
				t.Fatalf("title result=%+v err=%v", result, err)
			}
			for i, field := range []string{"slug", "branch"} {
				canonical, _ := db.CanonicalManifest(m.ID)
				changed := canonical.Manifest
				if field == "slug" {
					if state == model.StateClosed {
						continue
					}
					changed.Slug = "new-name"
				} else {
					changed.Checkout.Specification.Branch = "new-branch"
				}
				payload, _ := json.Marshal(changed)
				_, err := db.ExecuteStoreCommand(StoreCommand{ID: fmt.Sprintf("unsafe-%d", i), ProtocolVersion: ProtocolVersion, Operation: StoreManifestSave, ItemID: m.ID, ExpectedRevision: &canonical.Revision, PayloadDigest: StorePayloadDigest(payload)}, payload)
				if err == nil {
					t.Fatalf("%s change bypassed naming policy", field)
				}
				after, _ := db.CanonicalManifest(m.ID)
				if after.Revision != canonical.Revision || !reflect.DeepEqual(after.Manifest, canonical.Manifest) {
					t.Fatal("rejected mutation changed canonical state")
				}
			}
		})
	}
}

func TestArchiveRestoreAllocatesSlugWithoutRenamingBranch(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	m := model.NewManifest(importTestID, "original", "Original", nil, false, time.Now(), model.Repository{}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "original-branch"))
	if err := native.CreateItem(context.Background(), m); err != nil {
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
	command := func(id, typ, title, state string) ManifestCommandResult {
		t.Helper()
		canonical, _ := db.CanonicalManifest(m.ID)
		result, err := db.ExecuteManifestCommand(ManifestCommand{ID: id, ProtocolVersion: ProtocolVersion, Type: typ, ItemID: m.ID, ExpectedRevision: &canonical.Revision, Title: title, TargetState: state, CreatedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	archived := command("archive", CommandStateSet, "", model.StateClosed)
	if archived.Manifest.Slug != "" {
		t.Fatal("archive retained an active slug")
	}
	command("title", CommandTitleSet, "Different title", "")
	restored := command("restore", CommandStateSet, "", model.StateBacklog)
	if restored.Manifest.ID != m.ID || restored.Manifest.Slug != "different-title" || restored.Manifest.Checkout.Specification.Branch != m.Checkout.Specification.Branch {
		t.Fatalf("restore=%+v", restored.Manifest)
	}
	canonical, _ := db.CanonicalManifest(m.ID)
	if _, err := db.ExecuteManifestCommand(ManifestCommand{ID: "obsolete", ProtocolVersion: ProtocolVersion, Type: "item.naming.finalize", ItemID: m.ID, ExpectedRevision: &canonical.Revision, Title: "Do not rename", CreatedAt: time.Now()}); err == nil {
		t.Fatal("obsolete naming command accepted")
	}
}
