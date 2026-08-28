package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestStoreCommandsCommitCanonicalStateWithoutSensitivePayloads(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := model.NewManifest(importTestID, "generic", "Generic", nil, false, now, model.Repository{}, model.Checkout{})
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

	canonical, _ := db.CanonicalManifest(importTestID)
	manifest.Title = "Updated"
	manifest.UpdatedAt = now.Add(time.Second)
	payload, _ := json.Marshal(manifest)
	command := StoreCommand{ID: "manifest-generic", ProtocolVersion: ProtocolVersion, Operation: StoreManifestSave, ItemID: importTestID, ExpectedRevision: &canonical.Revision, PayloadDigest: StorePayloadDigest(payload), CreatedAt: now.Add(time.Second)}
	if err := StageStoreMutation(db, command, payload); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecuteStoreCommand(command, payload)
	if err != nil || result.Revision != canonical.Revision+1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	current, _ := db.CanonicalManifest(importTestID)
	if current.Manifest.Title != "Updated" {
		t.Fatalf("canonical manifest=%+v", current.Manifest)
	}
	if disk, err := native.LoadManifest(importTestID); err != nil || disk.Title != "Generic" {
		t.Fatalf("canonical manifest was written to disk: %+v err=%v", disk, err)
	}

	sentinel := "private-event-path-and-error-sentinel-92d1"
	event := model.NewEvent(now.Add(2*time.Second), "runtime.failed", "wi", map[string]any{"error": sentinel, "path": "/" + sentinel})
	payload, _ = json.Marshal(event)
	eventCommand := StoreCommand{ID: "event-generic", ProtocolVersion: ProtocolVersion, Operation: StoreEventAppend, ItemID: importTestID, ExpectedRevision: &result.Revision, PayloadDigest: StorePayloadDigest(payload), CreatedAt: now.Add(2 * time.Second)}
	if err := StageStoreMutation(db, eventCommand, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecuteStoreCommand(eventCommand, payload); err != nil {
		t.Fatal(err)
	}

	runtime := model.AgentRuntime{ID: "runtime-1", WorkItemID: importTestID, Mode: "rpc", State: model.AgentRuntimeProblem, StartedAt: now, UpdatedAt: now.Add(3 * time.Second)}
	payload, _ = json.Marshal(runtime)
	current, _ = db.CanonicalManifest(importTestID)
	runtimeCommand := StoreCommand{ID: "runtime-generic", ProtocolVersion: ProtocolVersion, Operation: StoreAgentRuntimeSave, ItemID: importTestID, ExpectedRevision: &current.Revision, PayloadDigest: StorePayloadDigest(payload), CreatedAt: now.Add(3 * time.Second)}
	if err := StageStoreMutation(db, runtimeCommand, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecuteStoreCommand(runtimeCommand, payload); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeNativeWrites(context.Background(), db, native); err != nil {
		t.Fatal(err)
	}
	var projectedRuntime model.AgentRuntime
	if found, err := db.ReadProjection(RuntimeOwnershipProjection, importTestID, &projectedRuntime); err != nil || !found {
		t.Fatalf("unsafe projected runtime=%+v found=%v err=%v", projectedRuntime, found, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(DatabasePath(root))
	if strings.Contains(string(contents), sentinel) {
		t.Fatal("unrestricted event content entered wi.db")
	}
}
