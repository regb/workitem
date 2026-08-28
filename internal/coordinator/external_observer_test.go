package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	processpkg "github.com/regb/workitem/internal/process"
	"github.com/regb/workitem/internal/store"
)

func TestExternalObserverVerifiesRuntimeAndBuildsAgentProjection(t *testing.T) {
	root, itemID := t.TempDir(), importTestID
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := model.NewManifest(itemID, "observer", "Observer", nil, false, now, model.Repository{}, model.Checkout{})
	manifest.State = model.StateWorking
	manifest.RootPiSession = &model.PiSession{ID: "session-1", Path: "sessions/pi/session-1.jsonl"}
	if err := native.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	processInfo, err := processpkg.New().Info(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	runtime := model.AgentRuntime{ID: "runtime-test", WorkItemID: itemID, State: model.AgentRuntimeRunning, HostPID: os.Getpid(), HostProcessGroup: processInfo.PGRP, HostStartTime: processInfo.StartTime, StartedAt: now, UpdatedAt: now}
	if err := native.SaveAgentRuntime(context.Background(), itemID, runtime); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	encodedManifest, _ := json.Marshal(manifest)
	digest := sha256.Sum256(encodedManifest)
	if _, err := db.SyncImportedManifests(map[string]ImportedManifest{itemID: {Manifest: manifest, Digest: hex.EncodeToString(digest[:])}}, true); err != nil {
		t.Fatal(err)
	}
	last := now.Add(-time.Second)
	piIndex := PiSessionIndex{WorkItemID: itemID, SessionID: "session-1", Source: "items/" + itemID + "/sessions/pi/session-1.jsonl", ObservedAt: now, InferredTurnState: "idle", LastTurnActivity: &PiEventFact{Timestamp: last, Role: "assistant", Terminal: true}}
	encodedIndex, _ := json.Marshal(piIndex)
	safeRuntime := runtime
	encodedRuntime, _ := json.Marshal(safeRuntime)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: PiSessionProjection, Key: itemID, Value: encodedIndex}, {Projection: RuntimeOwnershipProjection, Key: itemID, Value: encodedRuntime}}); err != nil {
		t.Fatal(err)
	}
	observer := NewExternalObserver(db, root)
	if err := observer.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	var projection AgentObservation
	found, err := db.ReadProjection(AgentObservationProjection, itemID, &projection)
	if err != nil || !found || projection.Status != "idle" || !projection.ProcessOnline || projection.Worktree == nil || projection.Worktree.Status != "absent" || projection.WorktreeObservedAt.IsZero() {
		t.Fatalf("projection=%+v found=%v err=%v", projection, found, err)
	}
	worktreeObservedAt := projection.WorktreeObservedAt
	if err := observer.ReconcileRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if found, err = db.ReadProjection(AgentObservationProjection, itemID, &projection); err != nil || !found || projection.Worktree == nil || !projection.WorktreeObservedAt.Equal(worktreeObservedAt) {
		t.Fatalf("runtime-only projection=%+v found=%v err=%v", projection, found, err)
	}

	externalObservedAt := projection.ObservedAt
	promptAt := now.Add(time.Second)
	piIndex.InferredTurnState = "incomplete"
	piIndex.LastTurnActivity = &PiEventFact{Timestamp: promptAt, Role: "user"}
	piIndex.LastUserPrompt = piIndex.LastTurnActivity
	activity := AttentionActivity{WorkItemID: itemID, LastRequestedAt: &promptAt, ObservedAt: promptAt, Source: piIndex.Source}
	encodedIndex, _ = json.Marshal(piIndex)
	encodedActivity, _ := json.Marshal(activity)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: PiSessionProjection, Key: itemID, Value: encodedIndex}, {Projection: AttentionActivityProjection, Key: itemID, Value: encodedActivity}}); err != nil {
		t.Fatal(err)
	}
	if err := observer.ReconcileActionability(context.Background()); err != nil {
		t.Fatal(err)
	}
	if found, err = db.ReadProjection(AgentObservationProjection, itemID, &projection); err != nil || !found || projection.Status != "busy" || projection.Activity.LastRequestedAt == nil || !projection.Activity.LastRequestedAt.Equal(promptAt) || projection.Worktree == nil || !projection.ObservedAt.Equal(externalObservedAt) {
		t.Fatalf("refreshed projection=%+v found=%v err=%v", projection, found, err)
	}

	settledAt := promptAt.Add(time.Second)
	piIndex.InferredTurnState = "idle"
	piIndex.LastTurnActivity = &PiEventFact{Timestamp: settledAt, Role: "assistant", Terminal: true}
	staleAt := settledAt.Add(time.Second)
	staleRuntime := RuntimeActivity{WorkItemID: itemID, RuntimeID: "runtime-old", TurnState: "busy", LastEventAt: &staleAt, LastRequestedAt: &staleAt, ObservedAt: staleAt, Source: "daemon.runtime.live"}
	encodedIndex, _ = json.Marshal(piIndex)
	encodedStaleRuntime, _ := json.Marshal(staleRuntime)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: PiSessionProjection, Key: itemID, Value: encodedIndex}, {Projection: RuntimeActivityProjection, Key: itemID, Value: encodedStaleRuntime}}); err != nil {
		t.Fatal(err)
	}
	if err := observer.ReconcileActionability(context.Background()); err != nil {
		t.Fatal(err)
	}
	if found, err = db.ReadProjection(AgentObservationProjection, itemID, &projection); err != nil || !found || projection.Status != "idle" || projection.Reason != "latest assistant answer is terminal" {
		t.Fatalf("stale owner activity remained actionable: projection=%+v found=%v err=%v", projection, found, err)
	}
}
