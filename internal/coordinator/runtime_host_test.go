package coordinator

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func TestRuntimeHostDoesNotExposeOperatorCommands(t *testing.T) {
	for _, method := range []string{MethodCreateItem, MethodManifestCommand, MethodRegisterRepository, MethodRenameRepository, MethodLeaseAcquire} {
		if hostMethodAllowed(method) {
			t.Fatalf("host exposes %s", method)
		}
	}
	for _, method := range []string{MethodRuntimeEvent, MethodStoreCommand, MethodItemResources, MethodCanonicalManifest} {
		if !hostMethodAllowed(method) {
			t.Fatalf("host lacks %s", method)
		}
	}
}

// A runtime host may only publish the runtime record of the runtime that owns
// its item, under the item's current workspace claim.
func TestRuntimeHostPublishesOnlyItsOwnRuntime(t *testing.T) {
	other := "01KZYHGDCECSFS4BJ2SNTQP49W"
	manifest := testManifest(importTestID, "host", "Host", time.Now().UTC())
	manifest.Checkout.Claim = &model.WorkspaceClaim{ID: "claim", Generation: 2, Executor: model.ExecutorLocal, Path: "/work"}
	db, _ := itemDatabase(t, manifest, testManifest(other, "other", "Other", time.Now().UTC()))
	seedRuntimeOwner(t, db, importTestID, "runtime")
	server := &Server{database: db}
	own := model.AgentRuntime{ID: "runtime", WorkItemID: importTestID, State: model.AgentRuntimeRunning, WorkspaceClaimID: "claim", WorkspaceClaimGeneration: 2}
	request := func(operation, itemID string, runtime model.AgentRuntime) StoreCommandRequest {
		payload, _ := json.Marshal(runtime)
		return StoreCommandRequest{Command: StoreCommand{Operation: operation, ItemID: itemID}, Payload: payload}
	}
	if err := server.validateHostStoreMutation(request(StoreAgentRuntimeSave, importTestID, own)); err != nil {
		t.Fatalf("own runtime refused: %v", err)
	}
	replaced, otherItem, staleClaim := own, own, own
	replaced.ID = "replacement"
	otherItem.WorkItemID = other
	staleClaim.WorkspaceClaimGeneration = 1
	for name, refused := range map[string]StoreCommandRequest{
		"another operation": request(StoreManifestSave, importTestID, own),
		"another runtime":   request(StoreAgentRuntimeSave, importTestID, replaced),
		"another item":      request(StoreAgentRuntimeSave, other, otherItem),
		"a stale claim":     request(StoreAgentRuntimeSave, importTestID, staleClaim),
	} {
		if err := server.validateHostStoreMutation(refused); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
