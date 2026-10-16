package coordinator

import (
	"context"
	"encoding/json"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"testing"
	"time"
)

func TestRuntimeReadinessReasonsPersistAndAreOwnerFenced(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m := model.NewManifest(importTestID, "status", "Status", nil, false, now, model.Repository{}, model.Checkout{})
	m.State = model.StateActive
	if err := native.CreateItem(context.Background(), m); err != nil {
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
	event := RuntimeSemanticEvent{ID: "status:1", RuntimeID: "runtime-1", WorkItemID: importTestID, Type: "runtime.status", Timestamp: now.Add(2 * time.Second), Readiness: "waiting_input", ReasonCode: "session_start_pending"}
	if _, err := IngestRuntimeEvent(context.Background(), db, event); err != nil {
		t.Fatal(err)
	}
	var owner model.AgentRuntime
	_, _ = db.ReadProjection(RuntimeOwnershipProjection, importTestID, &owner)
	if owner.Readiness != "waiting_input" {
		t.Fatal(owner)
	}
	var observation AgentObservation
	_, _ = db.ReadProjection(AgentObservationProjection, importTestID, &observation)
	if observation.Status != "problem" || observation.Reason == "" {
		t.Fatal(observation)
	}
	stale := event
	stale.ID = "status:old"
	stale.Timestamp = now
	stale.Readiness = "ready"
	stale.ReasonCode = "editor_ready"
	if _, err := IngestRuntimeEvent(context.Background(), db, stale); err != nil {
		t.Fatal(err)
	}
	_, _ = db.ReadProjection(RuntimeOwnershipProjection, importTestID, &owner)
	if owner.Readiness != "waiting_input" {
		t.Fatal("stale readiness replaced newer state")
	}
	for _, mutate := range []func(*RuntimeSemanticEvent){func(e *RuntimeSemanticEvent) { e.ReasonCode = "native prompt or credential" }, func(e *RuntimeSemanticEvent) { e.Readiness = "unknown body" }, func(e *RuntimeSemanticEvent) { e.DeliveryState = "raw failure" }, func(e *RuntimeSemanticEvent) { e.RuntimeID = "another-owner" }} {
		bad := event
		bad.ID = "bad"
		mutate(&bad)
		if _, err := IngestRuntimeEvent(context.Background(), db, bad); err == nil {
			t.Fatal("accepted unsafe or stale status")
		}
	}
	command := event
	command.ID = "rejected:1"
	command.Type = "command.rejected"
	command.Timestamp = now.Add(3 * time.Second)
	command.Readiness = ""
	command.CommandID = "cmd"
	command.Failed = true
	command.ReasonCode = "paste_readback_missing"
	command.DeliveryState = "uncertain"
	if _, err := IngestRuntimeEvent(context.Background(), db, command); err != nil {
		t.Fatal(err)
	}
	_, _ = db.ReadProjection(AgentObservationProjection, importTestID, &observation)
	if observation.Status != "problem" {
		t.Fatal(observation)
	}
	retained, err := db.RuntimeEvents(importTestID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		ReasonCode    string `json:"reason_code"`
		DeliveryState string `json:"delivery_state"`
	}
	if err := json.Unmarshal(retained.Events[len(retained.Events)-1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ReasonCode != "paste_readback_missing" || payload.DeliveryState != "uncertain" {
		t.Fatal(payload)
	}
	// A delayed bootstrap callback loaded before the newer status arrived.
	bootstrap := owner
	bootstrap.Readiness = "starting"
	bootstrap.ReadinessReasonCode = "runtime_starting"
	bootstrap.ReadinessObservedAt = &now
	bootstrap.Capabilities.Headless = true
	bootstrap.Capabilities.Prompt = true
	raw, _ := json.Marshal(bootstrap)
	current, _ := db.CanonicalManifest(importTestID)
	save := StoreCommand{ID: "bootstrap-caps", ProtocolVersion: ProtocolVersion, Operation: StoreAgentRuntimeSave, ItemID: importTestID, ExpectedRevision: &current.Revision, PayloadDigest: StorePayloadDigest(raw), CreatedAt: time.Now().UTC()}
	if _, err := db.ExecuteStoreCommand(save, raw); err != nil {
		t.Fatal(err)
	}
	_, _ = db.ReadProjection(RuntimeOwnershipProjection, importTestID, &owner)
	if owner.Readiness != "waiting_input" || owner.ReadinessReasonCode != "session_start_pending" || !owner.Capabilities.Headless || !owner.Capabilities.Prompt {
		t.Fatal("bootstrap rolled back readiness or lost capabilities", owner)
	}
	// Readiness belongs to the owner, not to the item across generations.
	bootstrap.ID = "replacement-runtime"
	bootstrap.Readiness = ""
	bootstrap.ReadinessReasonCode = ""
	bootstrap.ReadinessObservedAt = nil
	raw, _ = json.Marshal(bootstrap)
	current, _ = db.CanonicalManifest(importTestID)
	save.ID = "replacement"
	save.ExpectedRevision = &current.Revision
	save.PayloadDigest = StorePayloadDigest(raw)
	if _, err := db.ExecuteStoreCommand(save, raw); err != nil {
		t.Fatal(err)
	}
	owner = model.AgentRuntime{}
	_, _ = db.ReadProjection(RuntimeOwnershipProjection, importTestID, &owner)
	if owner.Readiness != "" {
		t.Fatal("old readiness crossed owner fence", owner)
	}
}
