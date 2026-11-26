package coordinator

import (
	"testing"
	"time"

	"github.com/regb/workitem/internal/credential"
	"github.com/regb/workitem/internal/model"
)

// An item credential is valid only for the item's current runtime, until that
// runtime is asked to stop.
func TestItemCredentialEndsWithItsRuntime(t *testing.T) {
	db, _ := itemDatabase(t, testManifest(importTestID, "credential", "Credential", time.Now().UTC()))
	issued, err := credential.New(credential.Item, importTestID)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(value credential.Credential) bool {
		t.Helper()
		ok, err := db.VerifyItemCredential(value)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if err := db.SaveItemCredential(importTestID, "runtime", issued.Hash()); err != nil {
		t.Fatal(err)
	}
	if verify(issued) {
		t.Fatal("credential valid before its runtime owns the item")
	}
	seedRuntimeOwner(t, db, importTestID, "runtime")
	if !verify(issued) {
		t.Fatal("current runtime's credential refused")
	}
	forged, _ := credential.New(credential.Item, importTestID)
	operator, _ := credential.New(credential.Operator, "")
	if verify(forged) || verify(operator) {
		t.Fatal("a credential that was not issued to the item's runtime was accepted")
	}
	stopping := model.AgentRuntime{ID: "runtime", WorkItemID: importTestID, State: model.AgentRuntimeStopping}
	saveRuntimeOwner(t, db, stopping)
	if verify(issued) {
		t.Fatal("credential outlived its runtime's stop")
	}
	seedRuntimeOwner(t, db, importTestID, "replacement")
	if verify(issued) {
		t.Fatal("credential outlived a newer runtime taking the item over")
	}
}
