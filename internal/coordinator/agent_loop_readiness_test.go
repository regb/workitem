package coordinator

import (
	"encoding/json"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
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

func TestADraftInTheEditorIsNotAProblem(t *testing.T) {
	for _, reason := range []string{"editor_not_empty", "editor_not_visible"} {
		event := RuntimeSemanticEvent{Type: agent.EventRuntimeStatus, Readiness: "waiting_input", ReasonCode: reason, Timestamp: time.Now()}
		idle := AgentObservation{Status: "idle", TurnState: "idle"}
		applyLiveRuntimeObservation(&idle, event, RuntimeActivity{AgentState: "settled", TurnState: "idle", RuntimeState: model.AgentRuntimeRunning})
		if idle.Status != "idle" {
			t.Fatalf("%s made a settled agent a problem: %+v", reason, idle)
		}
		busy := AgentObservation{Status: "busy", TurnState: "incomplete"}
		applyLiveRuntimeObservation(&busy, event, RuntimeActivity{AgentState: "active", TurnState: "busy", RuntimeState: model.AgentRuntimeRunning})
		if busy.Status != "busy" {
			t.Fatalf("%s hid an active turn: %+v", reason, busy)
		}
	}
	// A session that never started still needs the operator.
	stuck := AgentObservation{Status: "idle"}
	applyLiveRuntimeObservation(&stuck, RuntimeSemanticEvent{Type: agent.EventRuntimeStatus, Readiness: "waiting_input", ReasonCode: "session_start_pending", Timestamp: time.Now()}, RuntimeActivity{})
	if stuck.Status != "problem" {
		t.Fatalf("pending session start was not a problem: %+v", stuck)
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

func TestAQuestionMidTurnNeedsAttentionNotAFix(t *testing.T) {
	for _, reason := range []string{"question_pending", "plan_approval_pending", "permission_pending"} {
		observation := AgentObservation{Status: "busy", TurnState: "incomplete"}
		activity := RuntimeActivity{AgentState: "active", TurnState: "busy", RuntimeState: model.AgentRuntimeRunning}
		applyLiveRuntimeObservation(&observation, RuntimeSemanticEvent{Type: agent.EventRuntimeStatus, Readiness: "waiting_input", ReasonCode: reason, Timestamp: time.Now()}, activity)
		if observation.Status != "idle" || observation.Reason == "" {
			t.Fatalf("%s: %+v", reason, observation)
		}
		// Answering resumes the turn.
		applyLiveRuntimeObservation(&observation, RuntimeSemanticEvent{Type: agent.EventRuntimeStatus, Readiness: "busy", ReasonCode: "active_turn", Timestamp: time.Now()}, activity)
		if observation.Status != "busy" {
			t.Fatalf("%s answered: %+v", reason, observation)
		}
	}
}

func TestObserverKeepsAQuestionAskedAfterTheLastRequest(t *testing.T) {
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
	started := time.Now().Add(-time.Second)
	asked := time.Now()
	owner.Readiness = "waiting_input"
	owner.ReadinessReasonCode = "question_pending"
	owner.ReadinessObservedAt = &asked
	activity := RuntimeActivity{RuntimeID: "owner", Source: "daemon.runtime.live", AgentState: "active", TurnState: "busy", LastRequestedAt: &started, LastEventAt: &asked}
	a, _ := json.Marshal(owner)
	b, _ := json.Marshal(activity)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: RuntimeOwnershipProjection, Key: importTestID, Value: a}, {Projection: RuntimeActivityProjection, Key: importTestID, Value: b}}); err != nil {
		t.Fatal(err)
	}
	observation := AgentObservation{ProcessOnline: true}
	if err := NewExternalObserver(db, root).applyActionability(importTestID, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Status != "idle" || observation.Reason != "Claude is waiting for your answer" {
		t.Fatalf("question was not left for the operator: %+v", observation)
	}
}

// Before an agent writes its first turn, only a runtime that should run but
// is gone is a problem: a starting agent is working, and an item with no
// agent yet waits for a message.
func TestAnAgentBeforeItsFirstTurnIsOnlyAProblemWhenItsRuntimeIsGone(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	observer := NewExternalObserver(db, root)
	status := func(online bool) string {
		t.Helper()
		observation := AgentObservation{ProcessOnline: online}
		if err := observer.applyActionability(importTestID, &observation); err != nil {
			t.Fatal(err)
		}
		return observation.Status
	}
	if got := status(false); got != "idle" {
		t.Fatalf("no agent yet: %s", got)
	}
	owner := model.AgentRuntime{ID: "owner", WorkItemID: importTestID, State: model.AgentRuntimeStarting, UpdatedAt: time.Now()}
	save := func() {
		t.Helper()
		encoded, _ := json.Marshal(owner)
		if err := db.UpdateProjections([]ProjectionUpdate{{Projection: RuntimeOwnershipProjection, Key: importTestID, Value: encoded}}); err != nil {
			t.Fatal(err)
		}
	}
	save()
	if got := status(false); got != "busy" {
		t.Fatalf("starting agent: %s", got)
	}
	owner.State = model.AgentRuntimeRunning
	owner.UpdatedAt = time.Now().Add(-time.Hour)
	save()
	if got := status(true); got != "busy" {
		t.Fatalf("running agent before its first turn: %s", got)
	}
	if got := status(false); got != "problem" {
		t.Fatalf("vanished agent: %s", got)
	}
	owner.State = model.AgentRuntimeStopped
	save()
	if got := status(false); got != "idle" {
		t.Fatalf("stopped agent before its first turn: %s", got)
	}
}

// A turn that stopped on a usage limit says so instead of a generic failure.
func TestAUsageLimitIsReportedAsSuch(t *testing.T) {
	activity := RuntimeActivity{}
	at := time.Now()
	header := runtimeEventHeader{Type: agent.EventAgentSettled, Timestamp: at, Failed: true, Outcome: "failed", ReasonCode: "usage_limit"}
	if !applyRuntimeActivity(&activity, header, importTestID, "owner") || activity.AgentState != "failed" || activity.FailureReason != "usage_limit" {
		t.Fatalf("activity=%+v", activity)
	}
	observation := AgentObservation{}
	applyLiveRuntimeObservation(&observation, RuntimeSemanticEvent{Type: agent.EventAgentSettled, Failed: true, ReasonCode: "usage_limit", Timestamp: at}, activity)
	if observation.Status != "problem" || observation.Reason != driverprotocol.RuntimeReasonText("usage_limit") {
		t.Fatalf("observation=%+v", observation)
	}
	// The next turn clears it.
	applyRuntimeActivity(&activity, runtimeEventHeader{Type: agent.EventAgentStarted, Timestamp: at.Add(time.Second)}, importTestID, "owner")
	if activity.FailureReason != "" {
		t.Fatalf("stale failure reason: %+v", activity)
	}
}
