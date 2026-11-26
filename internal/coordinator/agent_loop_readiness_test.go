package coordinator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

// liveActivity is the activity the events leave, from base.
func liveActivity(base RuntimeActivity, events ...RuntimeSemanticEvent) RuntimeActivity {
	for _, event := range events {
		applyRuntimeActivity(&base, runtimeEventHeader{Type: event.Type, Timestamp: event.Timestamp, Failed: event.Failed, Outcome: event.Outcome, Readiness: event.Readiness, ReasonCode: event.ReasonCode, UsageProvider: event.UsageProvider}, importTestID, "owner")
	}
	return base
}

// A live runtime event moves the prior observation, given the activity it
// leaves, to a status.
func TestLiveRuntimeObservation(t *testing.T) {
	now := time.Now()
	status := func(readiness, reason string) RuntimeSemanticEvent {
		return RuntimeSemanticEvent{Type: agent.EventRuntimeStatus, Readiness: readiness, ReasonCode: reason, Timestamp: now}
	}
	ready := func(kind, readiness, reason string) RuntimeSemanticEvent {
		return RuntimeSemanticEvent{Type: kind, Readiness: readiness, ReasonCode: reason, Timestamp: now}
	}
	failed := func(kind string) RuntimeSemanticEvent {
		return RuntimeSemanticEvent{Type: kind, Failed: true, Timestamp: now}
	}
	started := RuntimeSemanticEvent{Type: agent.EventAgentStarted, Timestamp: now}
	usage := RuntimeSemanticEvent{Type: agent.EventAgentSettled, Failed: true, Outcome: "failed", ReasonCode: "usage_limit", UsageProvider: "codex", Timestamp: now}
	prompt := status("awaiting_confirmation", "execution_pending")
	active := RuntimeActivity{AgentState: "active", TurnState: "busy", RuntimeState: model.AgentRuntimeRunning}
	settled := RuntimeActivity{AgentState: "settled", TurnState: "idle", RuntimeState: model.AgentRuntimeRunning}
	busy := AgentObservation{Status: "busy", TurnState: "incomplete"}
	idle := AgentObservation{Status: "idle", TurnState: "idle"}
	problem := AgentObservation{Status: "problem", TurnState: "failed"}
	type row struct {
		name     string
		prior    AgentObservation
		activity RuntimeActivity
		event    RuntimeSemanticEvent
		want     string
		turn     string
		check    func(AgentObservation) bool
	}
	rows := []row{
		{name: "input readiness does not settle an active loop", prior: busy, activity: active, event: status("ready", "interaction_required"), want: "busy"},
		{name: "blocked input in an active loop", prior: busy, activity: active, event: status("blocked", "interaction_required"), want: "problem"},
		{name: "required interaction in an active loop", prior: busy, activity: active, event: status("waiting_input", "interaction_required"), want: "problem"},
		{name: "busy readiness after required interaction", prior: AgentObservation{Status: "problem", TurnState: "incomplete"}, activity: active, event: status("busy", "interaction_required"), want: "busy"},
		{name: "a session that never started", prior: AgentObservation{Status: "idle"}, event: status("waiting_input", "session_start_pending"), want: "problem"},
		{name: "a question answered", prior: AgentObservation{Status: "idle", TurnState: "incomplete"}, activity: active, event: status("busy", "active_turn"), want: "busy"},
		{name: "a received prompt is work at once", prior: idle, activity: liveActivity(RuntimeActivity{AgentState: "settled", TurnState: "idle"}, prompt), event: prompt, want: "busy"},
		{name: "a command still being confirmed is not yet work", prior: idle, event: status("awaiting_confirmation", "command_submitted"), want: "idle"},
		{name: "a usage limit says so", activity: liveActivity(RuntimeActivity{}, usage), event: usage, want: "problem", check: func(o AgentObservation) bool {
			return o.Reason == driverprotocol.RuntimeReasonText("usage_limit") && o.FailureProvider == "codex"
		}},
	}
	for _, reason := range []string{"editor_not_empty", "editor_not_visible"} {
		rows = append(rows,
			row{name: reason + " in a settled agent is no problem", prior: idle, activity: settled, event: status("waiting_input", reason), want: "idle"},
			row{name: reason + " does not hide an active turn", prior: busy, activity: active, event: status("waiting_input", reason), want: "busy"})
	}
	for _, reason := range []string{"question_pending", "plan_approval_pending", "permission_pending"} {
		rows = append(rows, row{name: reason + " mid-turn needs attention, not a fix", prior: busy, activity: active, event: status("waiting_input", reason), want: "idle", check: func(o AgentObservation) bool { return o.Reason != "" }})
	}
	for _, kind := range []string{agent.EventAgentSettled, agent.EventTurnEnded} {
		rows = append(rows,
			row{name: "failed " + kind, prior: AgentObservation{Status: "busy"}, activity: liveActivity(RuntimeActivity{}, failed(kind)), event: failed(kind), want: "problem", turn: "failed"},
			row{name: "a start after failed " + kind, prior: problem, activity: liveActivity(RuntimeActivity{}, failed(kind), started), event: started, want: "busy"})
	}
	for _, kind := range []string{agent.EventRuntimeReady, agent.EventRuntimeStatus} {
		late, fresh := ready(kind, "ready", "headless_ready"), ready(kind, "busy", "active_turn")
		rows = append(rows,
			row{name: "late " + kind + " keeps a failed turn", prior: problem, activity: liveActivity(RuntimeActivity{}, failed(agent.EventAgentSettled), late), event: late, want: "problem", turn: "failed"},
			row{name: "busy " + kind + " after a failed turn is fresh work", prior: problem, activity: liveActivity(RuntimeActivity{}, failed(agent.EventAgentSettled), fresh), event: fresh, want: "busy", turn: "incomplete"})
	}
	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			observation := tt.prior
			applyLiveRuntimeObservation(&observation, tt.event, tt.activity)
			if observation.Status != tt.want || (tt.turn != "" && observation.TurnState != tt.turn) || (tt.check != nil && !tt.check(observation)) {
				t.Fatalf("observation=%+v, want status %s turn %q", observation, tt.want, tt.turn)
			}
		})
	}
}

// Runtime activity records a failure's reason until the next turn, and a
// received prompt as a request.
func TestRuntimeActivityFromLiveEvents(t *testing.T) {
	at := time.Now()
	activity := liveActivity(RuntimeActivity{}, RuntimeSemanticEvent{Type: agent.EventAgentSettled, Timestamp: at, Failed: true, Outcome: "failed", ReasonCode: "usage_limit", UsageProvider: "codex"})
	if activity.AgentState != "failed" || activity.FailureReason != "usage_limit" || activity.FailureProvider != "codex" {
		t.Fatalf("activity=%+v", activity)
	}
	if activity = liveActivity(activity, RuntimeSemanticEvent{Type: agent.EventAgentStarted, Timestamp: at.Add(time.Second)}); activity.FailureReason != "" || activity.FailureProvider != "" {
		t.Fatalf("stale failure reason: %+v", activity)
	}
	prompted := liveActivity(RuntimeActivity{AgentState: "settled", TurnState: "idle"}, RuntimeSemanticEvent{Type: agent.EventRuntimeStatus, Timestamp: at, Readiness: "awaiting_confirmation", ReasonCode: "execution_pending"})
	if prompted.TurnState != "busy" || prompted.LastRequestedAt == nil {
		t.Fatalf("prompted=%+v", prompted)
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
	if observation.Status != "idle" || observation.Reason != "The agent is waiting for your answer" {
		t.Fatalf("question was not left for the operator: %+v", observation)
	}
}

// Before an agent writes its first turn, only a runtime that should run but
// is gone is a problem: a starting agent is working, and an item with no
// agent yet waits for a message, as does an agent that is up and ready, or
// has run for agentStartLimit without writing anything.
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
	save()
	if got := status(true); got != "busy" {
		t.Fatalf("running agent coming up: %s", got)
	}
	owner.Readiness = "ready"
	save()
	if got := status(true); got != "idle" {
		t.Fatalf("ready agent before its first message: %s", got)
	}
	owner.Readiness = ""
	owner.StartedAt = time.Now().Add(-time.Hour)
	owner.UpdatedAt = owner.StartedAt
	save()
	if got := status(true); got != "idle" {
		t.Fatalf("agent up for an hour without a first turn: %s", got)
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



// Without live events, an unfinished turn reads busy only while the agent
// shows activity, or while a tool call awaits its result; a turn that died
// without a final record is a problem, never busy forever.
func TestAQuietUnfinishedTurnIsAProblem(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	observer := NewExternalObserver(db, root)
	owner, _ := json.Marshal(model.AgentRuntime{ID: "owner", WorkItemID: importTestID, State: model.AgentRuntimeRunning, StartedAt: time.Now().Add(-time.Hour)})
	status := func(last ConversationEventFact) AgentObservation {
		t.Helper()
		index, _ := json.Marshal(ConversationIndex{WorkItemID: importTestID, InferredTurnState: "incomplete", LastTurnActivity: &last})
		if err := db.UpdateProjections([]ProjectionUpdate{{Projection: RuntimeOwnershipProjection, Key: importTestID, Value: owner}, {Projection: ConversationProjection, Key: importTestID, Value: index}}); err != nil {
			t.Fatal(err)
		}
		observation := AgentObservation{WorkItemID: importTestID, ProcessOnline: true}
		if err := observer.applyActionability(importTestID, &observation); err != nil {
			t.Fatal(err)
		}
		return observation
	}
	if got := status(ConversationEventFact{Timestamp: time.Now().Add(-time.Minute), Role: "user"}); got.Status != "busy" {
		t.Fatalf("recent unfinished turn: %+v", got)
	}
	quiet := time.Now().Add(-agentQuietLimit - time.Minute)
	if got := status(ConversationEventFact{Timestamp: quiet, Role: "assistant", ContentTypes: []string{"text"}}); got.Status != "problem" || !strings.Contains(got.Reason, "never finished") {
		t.Fatalf("quiet unfinished turn: %+v", got)
	}
	if got := status(ConversationEventFact{Timestamp: quiet, Role: "assistant", ContentTypes: []string{"text", "tool_use"}}); got.Status != "busy" {
		t.Fatalf("long tool call: %+v", got)
	}
}
