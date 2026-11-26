package coordinator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func TestRuntimeEventsAndObservationsAreFencedByWorkspaceClaim(t *testing.T) {
	db, _ := itemDatabase(t)
	now := time.Now().UTC()
	owner := model.AgentRuntime{ID: "runtime-1", WorkItemID: importTestID, WorkspaceClaimID: "claim-1", WorkspaceClaimGeneration: 1}
	ownerBytes, _ := json.Marshal(owner)
	for _, claim := range []*model.WorkspaceClaim{nil, {ID: "claim-2", Generation: 2, Executor: model.ExecutorLocal, Path: "/workspace"}, {ID: "claim-1", Generation: 2, Executor: model.ExecutorLocal, Path: "/workspace"}} {
		m := model.NewManifest(importTestID, "claim-test", "Claim test", nil, false, now, model.Repository{}, model.Checkout{Specification: model.CheckoutSpecification{Kind: model.WorkspaceKindManagedSlot, Executor: model.ExecutorLocal, Branch: "wi/test"}, ClaimGeneration: 2, Claim: claim})
		raw, _ := json.Marshal(m)
		if err := db.UpdateProjections([]ProjectionUpdate{{Projection: ManifestProjection, Key: importTestID, Value: raw}, {Projection: RuntimeOwnershipProjection, Key: importTestID, Value: ownerBytes}}); err != nil {
			t.Fatal(err)
		}
		event := RuntimeSemanticEvent{ID: "runtime-1:stale", WorkItemID: importTestID, RuntimeID: owner.ID, Type: "turn.started", Timestamp: now}
		if _, err := IngestRuntimeEvent(context.Background(), db, event); err == nil || !strings.Contains(err.Error(), "superseded workspace claim") {
			t.Fatalf("claim=%+v error=%v", claim, err)
		}
		observation := AgentObservation{WorkItemID: importTestID, ProcessOnline: true}
		observer := &ExternalObserver{database: db}
		if err := observer.applyActionability(importTestID, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.Status != "problem" || !strings.Contains(observation.Reason, "superseded workspace claim") {
			t.Fatalf("observation=%+v", observation)
		}
	}
}
