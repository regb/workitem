package coordinator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestStateSetCommandEnforcesCapacityAndAllocatesRestoreSlug(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	candidate := model.NewManifest(importTestID, "domain", "Domain", nil, true, now, model.Repository{}, model.Checkout{})
	activeID := "01KZYHGDCECSFS4BJ2SNTQP49W"
	active := model.NewManifest(activeID, "active", "Active", nil, true, now, model.Repository{}, model.Checkout{})
	active.State = model.StateWorking
	for _, manifest := range []model.Manifest{candidate, active} {
		if err := native.CreateItem(context.Background(), manifest); err != nil {
			t.Fatal(err)
		}
	}
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := seedManifests(context.Background(), db, root); err != nil {
		t.Fatal(err)
	}
	canonical, _ := db.CanonicalManifest(candidate.ID)
	working := ManifestCommand{ID: "state-working", ProtocolVersion: ProtocolVersion, Type: CommandStateSet, ItemID: candidate.ID, ExpectedRevision: &canonical.Revision, Actor: "user", TargetState: model.StateWorking, EventType: "work_item.started", MaxActive: 1, CreatedAt: now.Add(time.Second)}
	if _, err := db.ExecuteManifestCommand(working); err == nil || !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("expected capacity error, got %v", err)
	}
	unchanged, _ := db.CanonicalManifest(candidate.ID)
	if unchanged.Manifest.State != model.StateBacklog || unchanged.Revision != canonical.Revision {
		t.Fatalf("rejected transition changed state: %+v", unchanged)
	}
	working.Force = true
	result, err := db.ExecuteManifestCommand(working)
	if err != nil || result.Manifest.State != model.StateWorking || result.Revision != canonical.Revision+2 || len(result.Events) != 2 || result.Events[1].Type != "work_item.started" {
		t.Fatalf("forced result=%+v err=%v", result, err)
	}
	archiveRevision := result.Revision
	archive, err := db.ExecuteManifestCommand(ManifestCommand{ID: "state-archive", ProtocolVersion: ProtocolVersion, Type: CommandStateSet, ItemID: candidate.ID, ExpectedRevision: &archiveRevision, Actor: "user", TargetState: model.StateArchived, MaxActive: 1, CreatedAt: now.Add(2 * time.Second)})
	if err != nil || archive.Manifest.State != model.StateArchived || archive.Manifest.Slug != "" {
		t.Fatalf("archive=%+v err=%v", archive, err)
	}
	// Once the original slug is active on another item, restore must allocate a
	// unique canonical alias in the same transaction.
	activeCanonical, _ := db.CanonicalManifest(activeID)
	renameRevision := activeCanonical.Revision
	if _, err := db.ExecuteManifestCommand(ManifestCommand{ID: "active-archive", ProtocolVersion: ProtocolVersion, Type: CommandStateSet, ItemID: activeID, ExpectedRevision: &renameRevision, Actor: "user", TargetState: model.StateArchived, CreatedAt: now.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeNativeWrites(context.Background(), db, native); err != nil {
		t.Fatal(err)
	}
	// Create a competing holder for "domain" through the canonical command path
	// so re-seeding from disk cannot clobber the candidate's committed state.
	holderID := "01KZYHGDCECSFS4BJ2SNTQP49X"
	holder := model.NewManifest(holderID, "domain", "Holder", nil, false, now, model.Repository{}, model.Checkout{})
	if _, err := db.ExecuteCreateItemCommand(CreateItemCommand{ID: "holder-create", ProtocolVersion: ProtocolVersion, Manifest: holder, DescriptionDigest: DescriptionDigest(""), Actor: "user", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	restoreRevision := archive.Revision
	restored, err := db.ExecuteManifestCommand(ManifestCommand{ID: "state-restore", ProtocolVersion: ProtocolVersion, Type: CommandStateSet, ItemID: candidate.ID, ExpectedRevision: &restoreRevision, Actor: "user", TargetState: model.StateBacklog, CreatedAt: now.Add(4 * time.Second)})
	if err != nil || restored.Manifest.State != model.StateBacklog || restored.Manifest.Slug != "domain-2" {
		t.Fatalf("restore=%+v err=%v", restored, err)
	}
	if restored.Manifest.StateChangedAt != now.Add(4*time.Second) {
		t.Fatalf("state_changed_at=%s", restored.Manifest.StateChangedAt)
	}
}

func TestWaitCommandComputesAndStoresResultingQueueAtomically(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	ids := []string{importTestID, "01KZYHGDCECSFS4BJ2SNTQP49W", "01KZYHGDCECSFS4BJ2SNTQP49X"}
	for index, id := range ids {
		manifest := model.NewManifest(id, "queue-"+string(rune('a'+index)), "Queue", nil, false, now.Add(time.Duration(index)*time.Second), model.Repository{}, model.Checkout{})
		manifest.State = model.StateWorking
		if err := native.CreateItem(context.Background(), manifest); err != nil {
			t.Fatal(err)
		}
	}
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := seedManifests(context.Background(), db, root); err != nil {
		t.Fatal(err)
	}
	for index, id := range ids {
		requestedAt := now.Add(time.Duration(index) * time.Minute)
		observation := AgentObservation{WorkItemID: id, Status: "idle", Activity: AttentionActivity{WorkItemID: id, LastRequestedAt: &requestedAt}, ObservedAt: now}
		encoded, _ := json.Marshal(observation)
		if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AgentObservationProjection, Key: id, Value: encoded}}); err != nil {
			t.Fatal(err)
		}
	}
	canonical, err := db.CanonicalManifest(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	command := ManifestCommand{ID: "wait-and-queue", ProtocolVersion: ProtocolVersion, Type: CommandStateSet, ItemID: ids[0], ExpectedRevision: &canonical.Revision, Actor: "user", TargetState: model.StateWaiting, EventType: "work_item.state_set", MaxActive: 2, CreatedAt: now.Add(5 * time.Minute)}
	result, err := db.ExecuteManifestCommandWithQueue(command, ActionabilityQueueOptions{})
	if err != nil || result.Command.Manifest.State != model.StateWaiting || result.Queue.Strategy != "recent-request" || len(result.Queue.Candidates) != 2 || result.Queue.Candidates[0].Manifest.ID != ids[2] || result.Queue.Candidates[1].Manifest.ID != ids[1] || !result.Selection.Found || result.Selection.WorkItemID != ids[2] || result.Selection.CurrentInQueue {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	// A retry returns the queue committed with the transition, even if the live
	// observation changes after the original acknowledgement.
	var changed AgentObservation
	if _, err := db.ReadProjection(AgentObservationProjection, ids[2], &changed); err != nil {
		t.Fatal(err)
	}
	changed.Status = "busy"
	encoded, _ := json.Marshal(changed)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AgentObservationProjection, Key: ids[2], Value: encoded}}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := db.ExecuteManifestCommandWithQueue(command, ActionabilityQueueOptions{})
	if err != nil || !duplicate.Command.Duplicate || len(duplicate.Queue.Candidates) != 2 || duplicate.Queue.Candidates[0].Manifest.ID != ids[2] {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	if _, err := db.ActionabilityQueue(ActionabilityQueueOptions{Strategy: "missing"}); err == nil {
		t.Fatal("unknown attention priority strategy was accepted")
	}
}
