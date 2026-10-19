package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

func TestNativeBranchObservationReplacesAbandonedFailureWithoutNewPrompt(t *testing.T) {
	db, root := attentionTestDatabase(t)
	seedRuntimeOwner(t, db, importTestID, "owner")
	ctx := context.Background()
	now := time.Now().UTC()
	failed := RuntimeSemanticEvent{ID: "aborted", RuntimeID: "owner", WorkItemID: importTestID, Type: agent.EventAgentSettled, Outcome: "failed", Failed: true, Timestamp: now}
	if _, err := IngestRuntimeEvent(ctx, db, failed); err != nil {
		t.Fatal(err)
	}
	// The append-only native index still ends at the abandoned aborted branch.
	index, _ := json.Marshal(ConversationIndex{WorkItemID: importTestID, InferredTurnState: "failed", LastTurnActivity: &ConversationEventFact{Timestamp: now, Role: "assistant", Terminal: true, Failed: true}})
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: ConversationProjection, Key: importTestID, Value: index}}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := loadRuntimeActivity(db, importTestID)
	tree := RuntimeSemanticEvent{ID: "tree-good", RuntimeID: "owner", WorkItemID: importTestID, Type: agent.EventRuntimeStatus, Readiness: "ready", AgentState: "idle", LeafID: "earlier-good", Timestamp: now.Add(time.Second)}
	if _, err := IngestRuntimeEvent(ctx, db, tree); err != nil {
		t.Fatal(err)
	}
	var observation AgentObservation
	if _, err := db.ReadProjection(AgentObservationProjection, importTestID, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Status != "idle" || observation.TurnState != "idle" {
		t.Fatal("tree change retained abandoned failure", observation)
	}
	after, _, _ := loadRuntimeActivity(db, importTestID)
	if after.AgentState != "idle" || after.LeafID != "earlier-good" || !after.LastCompletedAt.Equal(*before.LastCompletedAt) {
		t.Fatal("tree navigation fabricated a successful completion", after)
	}
	if err := NewExternalObserver(db, root).applyActionability(importTestID, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Status != "idle" {
		t.Fatal("chronological native tail overrode selected branch", observation)
	}
	// Replayed observations cannot overwrite a newer leaf, and duplicate delivery
	// does not append a second status event.
	older := tree
	older.ID = "old-tree"
	older.AgentState = "failed"
	older.LeafID = "aborted"
	older.Timestamp = now
	if _, err := IngestRuntimeEvent(ctx, db, older); err != nil {
		t.Fatal(err)
	}
	retained, _, _ := loadRuntimeActivity(db, importTestID)
	if retained.AgentState != "idle" || retained.LeafID != "earlier-good" {
		t.Fatal("stale snapshot changed the branch")
	}
	result, err := IngestRuntimeEvent(ctx, db, tree)
	if err != nil || !result.Duplicate {
		t.Fatal("snapshot retry lost idempotence", err)
	}
	seedRuntimeOwner(t, db, importTestID, "replacement")
	stale := tree
	stale.ID = "superseded"
	stale.Timestamp = now.Add(2 * time.Second)
	var mismatch *RuntimeOwnerMismatchError
	if _, err := IngestRuntimeEvent(ctx, db, stale); !errors.As(err, &mismatch) {
		t.Fatal("tree snapshot escaped owner fencing", err)
	}
}

func TestNativeObservationCannotBeUsedAsGenericReadyOrInvalidFacts(t *testing.T) {
	db, _ := attentionTestDatabase(t)
	seedRuntimeOwner(t, db, importTestID, "owner")
	now := time.Now().UTC()
	for index, mutate := range []func(*RuntimeSemanticEvent){
		func(e *RuntimeSemanticEvent) { e.AgentState = "arbitrary" },
		func(e *RuntimeSemanticEvent) { e.LeafID = "/private/native/session.jsonl" },
		func(e *RuntimeSemanticEvent) { e.LeafID = "native text is not an identifier" },
		func(e *RuntimeSemanticEvent) { e.Readiness = "busy" },
		func(e *RuntimeSemanticEvent) { e.Type = agent.EventRuntimeReady },
		func(e *RuntimeSemanticEvent) { e.AgentState = "" },
	} {
		event := RuntimeSemanticEvent{ID: string(rune('a' + index)), RuntimeID: "owner", WorkItemID: importTestID, Type: agent.EventRuntimeStatus, AgentState: "idle", LeafID: "leaf", Readiness: "ready", Timestamp: now}
		mutate(&event)
		if _, err := IngestRuntimeEvent(context.Background(), db, event); err == nil {
			t.Fatal("invalid native observation was retained", event)
		}
	}
}

func TestNativeBranchObservationCannotHealRuntimeInfrastructureFailure(t *testing.T) {
	db, _ := attentionTestDatabase(t)
	seedRuntimeOwner(t, db, importTestID, "owner")
	now := time.Now().UTC()
	events := []RuntimeSemanticEvent{
		{ID: "runtime-broken", RuntimeID: "owner", WorkItemID: importTestID, Type: agent.EventRuntimeFailed, Timestamp: now},
		{ID: "native-idle", RuntimeID: "owner", WorkItemID: importTestID, Type: agent.EventRuntimeStatus, AgentState: "idle", LeafID: "good", Readiness: "ready", Timestamp: now.Add(time.Second)},
	}
	for _, event := range events {
		if _, err := IngestRuntimeEvent(context.Background(), db, event); err != nil {
			t.Fatal(err)
		}
	}
	activity, _, _ := loadRuntimeActivity(db, importTestID)
	if activity.RuntimeState != model.AgentRuntimeProblem {
		t.Fatal("native idle erased infrastructure failure", activity)
	}
	var observation AgentObservation
	_, err := db.ReadProjection(AgentObservationProjection, importTestID, &observation)
	if err != nil || observation.Status != "problem" {
		t.Fatal("native idle concealed failed runtime", err, observation)
	}
}

func TestNativeObservationRetainsWorkspaceClaimFence(t *testing.T) {
	db, _ := attentionTestDatabase(t)
	owner := model.AgentRuntime{ID: "owner", WorkItemID: importTestID, WorkspaceClaimID: "superseded", WorkspaceClaimGeneration: 1}
	data, _ := json.Marshal(owner)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: RuntimeOwnershipProjection, Key: importTestID, Value: data}}); err != nil {
		t.Fatal(err)
	}
	event := RuntimeSemanticEvent{ID: "tree-stale-claim", RuntimeID: "owner", WorkItemID: importTestID, Type: agent.EventRuntimeStatus, Readiness: "ready", AgentState: "idle", LeafID: "leaf", Timestamp: time.Now().UTC()}
	if _, err := IngestRuntimeEvent(context.Background(), db, event); err == nil {
		t.Fatal("native snapshot bypassed the workspace generation fence")
	}
}
