package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestCreateItemCommandKeepsDescriptionOutOfDatabaseAndMaterializesNativeItem(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	description := "private-description-sentinel-7b9f"
	now := time.Now().UTC()
	manifest := model.NewManifest(importTestID, "created", "Created", []string{"backend"}, false, now, model.Repository{RootAtCreation: "/repo", GitCommonDir: "/repo/.git", CreatedFromCommit: strings.Repeat("a", 40)}, model.Checkout{Kind: model.WorkspaceKindManagedSlot, Branch: model.ItemBranchName("created", importTestID)})
	command := CreateItemCommand{ID: "create-command", ProtocolVersion: ProtocolVersion, Manifest: manifest, DescriptionDigest: DescriptionDigest(description), Actor: "user", CreatedAt: now}
	if err := StageDescription(db, command.ID, description); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecuteCreateItemCommand(command)
	if err != nil || result.Revision != 1 || result.Event.Type != "work_item.created" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(native.ItemDir(importTestID)); !os.IsNotExist(err) {
		t.Fatalf("native item dir existed before materialization: %v", err)
	}
	if err := MaterializeNativeWrites(context.Background(), db, native); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(native.ManifestPath(importTestID)); !os.IsNotExist(err) {
		t.Fatalf("canonical manifest was written to disk: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(native.ItemDir(importTestID), model.DescriptionFilename))
	if err != nil || string(contents) != description {
		t.Fatalf("description=%q err=%v", contents, err)
	}
	duplicate, err := db.ExecuteCreateItemCommand(command)
	if err != nil || !duplicate.Duplicate || duplicate.Manifest.ID != manifest.ID {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	different := command
	different.DescriptionDigest = DescriptionDigest("different")
	if _, err := db.ExecuteCreateItemCommand(different); err == nil {
		t.Fatal("expected reused create command id with different description digest to fail")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	databaseBytes, err := os.ReadFile(DatabasePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(databaseBytes), description) {
		t.Fatal("description content was copied into wi.db")
	}
}

func TestCleanupOrphanedStagesRemovesUncommittedPayload(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := StageDescription(db, "orphan", "private"); err != nil {
		t.Fatal(err)
	}
	if err := CleanupOrphanedStages(db); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".coordinator-pending"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("orphan stages remain: %+v", entries)
	}
}

func TestCreateItemCommandRejectsCanonicalClaimCollision(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	existing := model.NewManifest(importTestID, "collision", "Existing", nil, false, now, model.Repository{}, model.Checkout{})
	if err := native.CreateItem(context.Background(), existing); err != nil {
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
	newID := "01KZYHGDCECSFS4BJ2SNTQP49W"
	candidate := model.NewManifest(newID, "collision", "Candidate", nil, false, now, model.Repository{}, model.Checkout{})
	_, err = db.ExecuteCreateItemCommand(CreateItemCommand{ID: "collision-command", ProtocolVersion: ProtocolVersion, Manifest: candidate, DescriptionDigest: DescriptionDigest(""), Actor: "user", CreatedAt: now})
	if err == nil || !strings.Contains(err.Error(), "slug") {
		t.Fatalf("expected slug collision, got %v", err)
	}
	if _, err := db.CanonicalManifest(newID); err == nil {
		t.Fatal("rejected create produced canonical item")
	}
}
