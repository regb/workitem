package coordinator

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

func TestInputReadinessDoesNotSettleAnActiveAgentLoop(t *testing.T) {
	activity := RuntimeActivity{AgentState: "active", TurnState: "busy", RuntimeState: model.AgentRuntimeRunning}
	observation := AgentObservation{Status: "busy", TurnState: "incomplete"}
	for _, tt := range []struct{ readiness, want string }{{"ready", "busy"}, {"blocked", "problem"}, {"waiting_input", "problem"}, {"busy", "busy"}} {
		applyLiveRuntimeObservation(&observation, RuntimeSemanticEvent{Type: agent.EventRuntimeStatus, Readiness: tt.readiness, ReasonCode: "interaction_required", Timestamp: time.Now()}, activity)
		if observation.Status != tt.want {
			t.Fatalf("readiness=%s observation=%+v want=%s", tt.readiness, observation, tt.want)
		}
	}
}

func TestCurrentInputBlockTakesPriorityWithoutHidingLaterSettlement(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedRuntimeOwner(t, db, importTestID, "owner")
	var owner model.AgentRuntime
	if found, err := db.ReadProjection(RuntimeOwnershipProjection, importTestID, &owner); err != nil || !found {
		t.Fatalf("owner found=%v err=%v", found, err)
	}
	now := time.Now()
	started := now.Add(-time.Second)
	blocked := now
	completed := now.Add(time.Second)
	owner.Readiness = "waiting_input"
	owner.ReadinessReasonCode = "interaction_required"
	owner.ReadinessObservedAt = &blocked
	activity := RuntimeActivity{RuntimeID: "owner", Source: "daemon.runtime.live", AgentState: "active", TurnState: "busy", LastRequestedAt: &started, LastEventAt: &blocked}
	observer := NewExternalObserver(db, root)
	save := func() {
		t.Helper()
		a, _ := json.Marshal(owner)
		b, _ := json.Marshal(activity)
		if err := db.UpdateProjections([]ProjectionUpdate{{Projection: RuntimeOwnershipProjection, Key: importTestID, Value: a}, {Projection: RuntimeActivityProjection, Key: importTestID, Value: b}}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	observation := AgentObservation{ProcessOnline: true}
	if err := observer.applyActionability(importTestID, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Status != "problem" {
		t.Fatalf("active hook masked input block: %+v", observation)
	}
	activity.AgentState = "settled"
	activity.TurnState = "idle"
	activity.LastCompletedAt = &completed
	activity.LastEventAt = &completed
	save()
	if err := observer.applyActionability(importTestID, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Status != "idle" {
		t.Fatalf("old input block masked settlement: %+v", observation)
	}
}
