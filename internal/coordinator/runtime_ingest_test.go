package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestLiveRuntimeEventImmediatelyUpdatesActionability(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := model.NewManifest(importTestID, "live", "Live", nil, false, now, model.Repository{}, model.Checkout{})
	manifest.State = model.StateWorking
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
	seedRuntimeOwner(t, db, importTestID, "runtime-1")
	started := RuntimeSemanticEvent{ID: "runtime-1:event-1", RuntimeID: "runtime-1", WorkItemID: importTestID, Type: "turn.started", Timestamp: now.Add(time.Second), RequestID: "request-1"}
	result, err := IngestRuntimeEvent(context.Background(), db, started)
	if err != nil || result.Duplicate {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var projected AgentObservation
	if found, err := db.ReadProjection(AgentObservationProjection, importTestID, &projected); err != nil || !found || projected.Status != "busy" || projected.TurnState != "incomplete" {
		t.Fatalf("projected=%+v found=%v err=%v", projected, found, err)
	}
	var activity RuntimeActivity
	if found, err := db.ReadProjection(RuntimeActivityProjection, importTestID, &activity); err != nil || !found || activity.TurnState != "busy" || activity.Source != "daemon.runtime.live" {
		t.Fatalf("activity=%+v found=%v err=%v", activity, found, err)
	}
	duplicate, err := IngestRuntimeEvent(context.Background(), db, started)
	if err != nil || !duplicate.Duplicate || duplicate.ItemRevision != result.ItemRevision {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}

	settled := RuntimeSemanticEvent{ID: "runtime-1:event-2", RuntimeID: "runtime-1", WorkItemID: importTestID, Type: "agent.settled", Timestamp: now.Add(2 * time.Second)}
	if _, err := IngestRuntimeEvent(context.Background(), db, settled); err != nil {
		t.Fatal(err)
	}
	_, _ = db.ReadProjection(AgentObservationProjection, importTestID, &projected)
	if projected.Status != "idle" || projected.TurnState != "idle" {
		t.Fatalf("settled projection=%+v", projected)
	}
	if _, err := IngestRuntimeEvent(context.Background(), db, RuntimeSemanticEvent{ID: "runtime-1:event-3", RuntimeID: "runtime-1", WorkItemID: importTestID, Type: "message.delta", Timestamp: now}); err == nil {
		t.Fatal("expected verbose delta rejection")
	}
}

func TestRuntimeReadyFromNewOwnerResetsPreviousBusyState(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := model.NewManifest(importTestID, "handoff", "Handoff", nil, false, now, model.Repository{}, model.Checkout{})
	manifest.State = model.StateWorking
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
	seedRuntimeOwner(t, db, importTestID, "runtime-old")
	if _, err := IngestRuntimeEvent(context.Background(), db, RuntimeSemanticEvent{ID: "runtime-old:event-1", RuntimeID: "runtime-old", WorkItemID: importTestID, Type: "turn.started", Timestamp: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	seedRuntimeOwner(t, db, importTestID, "runtime-new")
	if _, err := IngestRuntimeEvent(context.Background(), db, RuntimeSemanticEvent{ID: "runtime-new:event-1", RuntimeID: "runtime-new", WorkItemID: importTestID, Type: "runtime.ready", Timestamp: now.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	var activity RuntimeActivity
	if found, err := db.ReadProjection(RuntimeActivityProjection, importTestID, &activity); err != nil || !found {
		t.Fatalf("activity found=%v err=%v", found, err)
	}
	if activity.RuntimeID != "runtime-new" || activity.TurnState != "idle" || activity.LastRequestedAt != nil {
		t.Fatalf("new runtime inherited stale activity: %+v", activity)
	}
	var observation AgentObservation
	if found, err := db.ReadProjection(AgentObservationProjection, importTestID, &observation); err != nil || !found {
		t.Fatalf("observation found=%v err=%v", found, err)
	}
	if observation.Status != "idle" || observation.TurnState != "idle" || observation.Reason != "runtime is ready" {
		t.Fatalf("new runtime inherited stale observation: %+v", observation)
	}
	_, err = IngestRuntimeEvent(context.Background(), db, RuntimeSemanticEvent{ID: "runtime-old:event-2", RuntimeID: "runtime-old", WorkItemID: importTestID, Type: "turn.started", Timestamp: now.Add(3 * time.Second)})
	var mismatch *RuntimeOwnerMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("superseded runtime event error = %v", err)
	}
}

func seedRuntimeOwner(t *testing.T, db *Database, itemID, runtimeID string) {
	t.Helper()
	encoded, err := json.Marshal(model.AgentRuntime{ID: runtimeID, WorkItemID: itemID, State: model.AgentRuntimeRunning})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: RuntimeOwnershipProjection, Key: itemID, Value: encoded}}); err != nil {
		t.Fatal(err)
	}
}
