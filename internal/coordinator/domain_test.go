package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestManifestCommandCommitsAtomicallyAndIdempotently(t *testing.T) {
	root, itemID := t.TempDir(), importTestID
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := model.NewManifest(itemID, "domain", "Domain command", nil, false, now, model.Repository{}, model.Checkout{})
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
	canonical, err := db.CanonicalManifest(itemID)
	if err != nil || canonical.Revision != 1 {
		t.Fatalf("canonical=%+v err=%v", canonical, err)
	}
	command := ManifestCommand{ID: "command-labels", ProtocolVersion: ProtocolVersion, Type: CommandLabelsAdd, ItemID: itemID, ExpectedRevision: &canonical.Revision, Actor: "user", Labels: []string{"backend", "urgent"}, CreatedAt: now.Add(time.Second)}
	result, err := db.ExecuteManifestCommand(command)
	if err != nil || !result.Changed || result.Revision != 3 || len(result.Events) != 2 || !model.HasLabel(result.Manifest.Labels, "backend") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if writes, _ := db.PendingNativeWrites(); len(writes) != 0 {
		t.Fatalf("manifest-only command scheduled a native write: %+v", writes)
	}
	duplicate, err := db.ExecuteManifestCommand(command)
	if err != nil || !duplicate.Duplicate || duplicate.Revision != result.Revision {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	different := command
	different.Type = CommandLabelsRemove
	if _, err := db.ExecuteManifestCommand(different); err == nil {
		t.Fatal("expected reused command id with different input to be rejected")
	}
}

func TestManifestCommandRejectsStaleRevisionAtomically(t *testing.T) {
	root, itemID := t.TempDir(), importTestID
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	manifest := model.NewManifest(itemID, "conflict", "Conflict", nil, false, time.Now().UTC(), model.Repository{}, model.Checkout{})
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
	stale := uint64(0)
	deep := true
	_, err = db.ExecuteManifestCommand(ManifestCommand{ID: "stale", ProtocolVersion: ProtocolVersion, Type: CommandDeepWorkSet, ItemID: itemID, ExpectedRevision: &stale, DeepWork: &deep})
	if err == nil {
		t.Fatal("expected revision conflict")
	}
	canonical, _ := db.CanonicalManifest(itemID)
	if canonical.Manifest.DeepWork || canonical.Revision != 1 {
		t.Fatalf("stale command changed canonical state: %+v", canonical)
	}
	if writes, _ := db.PendingNativeWrites(); len(writes) != 0 {
		t.Fatalf("stale command scheduled a native write: %+v", writes)
	}
}
