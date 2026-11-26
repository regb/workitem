package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

func TestConversationIndexTracksBackgroundWorkOfTheCurrentProcess(t *testing.T) {
	start := time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC)
	index := ConversationIndex{}
	attention := AttentionActivity{}
	apply := func(at time.Time, started, ended []string) {
		applyConversationFact(&index, &attention, ConversationEventFact{Role: "user", Timestamp: at, BackgroundStarted: started, BackgroundEnded: ended})
	}
	apply(start.Add(-time.Hour), []string{"old"}, nil)
	apply(start.Add(time.Minute), []string{"a", "b"}, nil)
	apply(start.Add(2*time.Minute), nil, []string{"a", "unknown"})
	// Work started by an earlier agent process died with it.
	if got := index.PendingBackground(start); got != 1 {
		t.Fatalf("pending since start = %d, background %v", got, index.Background)
	}
	apply(start.Add(3*time.Minute), nil, []string{"b"})
	if got := index.PendingBackground(start); got != 0 {
		t.Fatalf("pending after end = %d", got)
	}
	for i := 0; i < maxPendingBackground+10; i++ {
		apply(start.Add(time.Duration(i)*time.Second), []string{fmt.Sprintf("t%d", i)}, nil)
	}
	if len(index.Background) != maxPendingBackground {
		t.Fatalf("background kept %d entries", len(index.Background))
	}
}

func TestAnIdleAgentWaitingOnBackgroundWorkCountsIt(t *testing.T) {
	db, root := attentionTestDatabase(t)
	started := time.Now().UTC().Add(-time.Hour)
	owner, _ := json.Marshal(model.AgentRuntime{ID: "owner", WorkItemID: importTestID, State: model.AgentRuntimeRunning, StartedAt: started})
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: RuntimeOwnershipProjection, Key: importTestID, Value: owner}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	index, _ := json.Marshal(ConversationIndex{WorkItemID: importTestID, InferredTurnState: "idle", LastTurnActivity: &ConversationEventFact{Timestamp: now.Add(-time.Minute), Role: "assistant", Terminal: true}, Background: map[string]time.Time{"agent": now.Add(-2 * time.Minute), "earlier-process": started.Add(-time.Minute)}})
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: ConversationProjection, Key: importTestID, Value: index}}); err != nil {
		t.Fatal(err)
	}
	if _, err := IngestRuntimeEvent(context.Background(), db, RuntimeSemanticEvent{ID: "settled", RuntimeID: "owner", WorkItemID: importTestID, Type: agent.EventAgentSettled, Outcome: "completed", Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	observe := func(online bool) AgentObservation {
		t.Helper()
		observation := AgentObservation{ProcessOnline: online}
		if err := NewExternalObserver(db, root).applyActionability(importTestID, &observation); err != nil {
			t.Fatal(err)
		}
		return observation
	}
	if o := observe(true); o.Status != "idle" || o.BackgroundTasks != 1 || len(o.Background) != 1 || o.Background[0].ID != "agent" {
		t.Fatalf("online idle agent: %+v", o)
	}
	// Background work dies with the agent process.
	if o := observe(false); o.BackgroundTasks != 0 {
		t.Fatalf("offline agent: %+v", o)
	}
}

func TestBackgroundWorkKeepsWhatItIs(t *testing.T) {
	start := time.Date(2026, 11, 26, 15, 0, 0, 0, time.UTC)
	index := ConversationIndex{}
	attention := AttentionActivity{}
	apply := func(fact ConversationEventFact) { applyConversationFact(&index, &attention, fact) }
	apply(ConversationEventFact{Role: "assistant", Timestamp: start, BackgroundLabels: map[string]string{"call-ci": "Rerun failed\nCI job", "call-unused": "never launched"}})
	apply(ConversationEventFact{Role: "user", Timestamp: start.Add(time.Second), BackgroundStarted: []string{"ci"}, BackgroundCalls: map[string]string{"ci": "call-ci"}})
	apply(ConversationEventFact{Role: "user", Timestamp: start.Add(2 * time.Second), BackgroundStarted: []string{"agent"}, BackgroundLabels: map[string]string{"agent": "Map the core"}, BackgroundCalls: map[string]string{"agent": "call-agent"}})
	apply(ConversationEventFact{Role: "user", Timestamp: start.Add(3 * time.Second), BackgroundStarted: []string{"bare"}})
	work := index.PendingBackgroundWork(start)
	want := []BackgroundWork{{ID: "ci", Label: "Rerun failed CI job", StartedAt: start.Add(time.Second)}, {ID: "agent", Label: "Map the core", StartedAt: start.Add(2 * time.Second)}, {ID: "bare", StartedAt: start.Add(3 * time.Second)}}
	if fmt.Sprint(work) != fmt.Sprint(want) {
		t.Fatalf("work = %+v", work)
	}
	if _, kept := index.BackgroundCallLabels["call-ci"]; kept {
		t.Fatal("a started call's label stayed pending")
	}
	apply(ConversationEventFact{Role: "user", Timestamp: start.Add(time.Minute), BackgroundEnded: []string{"ci"}})
	if _, kept := index.BackgroundLabels["ci"]; kept || index.PendingBackground(start) != 2 {
		t.Fatalf("ended work kept: %+v", index)
	}
}

func TestAnOwnerWorksWhileADelegationWorks(t *testing.T) {
	db, _ := attentionTestDatabase(t)
	at := time.Now().UTC()
	manifest := func(id, owner string, state string) model.Manifest {
		m := model.NewManifest(id, id, id, nil, false, at, model.Repository{}, model.Checkout{})
		m.State, m.Owner = state, owner
		return m
	}
	ids := map[string]string{}
	for _, name := range []string{"owner", "busy", "grand", "middle", "asking", "idle", "closed", "lone"} {
		id, err := model.NewID()
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	manifests := []model.Manifest{
		manifest(ids["owner"], "", model.StateActive),
		manifest(ids["middle"], "", model.StateActive),
		manifest(ids["grand"], model.ItemPrincipal(ids["middle"]), model.StateActive),
		manifest(ids["busy"], model.ItemPrincipal(ids["owner"]), model.StateActive),
		manifest(ids["lone"], "", model.StateActive),
		manifest(ids["asking"], model.ItemPrincipal(ids["lone"]), model.StateActive),
		manifest(ids["idle"], model.ItemPrincipal(ids["lone"]), model.StateActive),
		manifest(ids["closed"], model.ItemPrincipal(ids["lone"]), model.StateClosed),
	}
	observe := func(name string, o AgentObservation) ProjectionUpdate {
		encoded, _ := json.Marshal(o)
		return ProjectionUpdate{Projection: AgentObservationProjection, Key: ids[name], Value: encoded}
	}
	if err := db.UpdateProjections([]ProjectionUpdate{
		observe("owner", AgentObservation{Status: "idle"}),
		observe("busy", AgentObservation{Status: "busy"}),
		observe("middle", AgentObservation{Status: "idle"}),
		observe("grand", AgentObservation{Status: "idle", BackgroundTasks: 1}),
		observe("lone", AgentObservation{Status: "idle"}),
		observe("asking", AgentObservation{Status: "busy", Runtime: &model.AgentRuntime{Readiness: "waiting_input"}}),
		observe("idle", AgentObservation{Status: "idle"}),
		observe("closed", AgentObservation{Status: "busy"}),
	}); err != nil {
		t.Fatal(err)
	}
	var working map[string]bool
	err := db.db.View(func(tx *bolt.Tx) error {
		var err error
		working, err = workingItems(manifests, tx.Bucket(bucketProjections).Bucket([]byte(AgentObservationProjection)))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"owner": true, "busy": true, "middle": true, "grand": true, "lone": false, "asking": false, "idle": false} {
		if working[ids[name]] != want {
			t.Errorf("%s: working=%v, want %v", name, working[ids[name]], want)
		}
	}
}

func TestClaudeToolResultAndInterruptionIndex(t *testing.T) {
	index := ConversationIndex{}
	attention := AttentionActivity{}
	at := time.Now()
	applyConversationFact(&index, &attention, ConversationEventFact{Role: "user", Timestamp: at, ContentTypes: []string{"text"}})
	applyConversationFact(&index, &attention, ConversationEventFact{Role: "assistant", Timestamp: at.Add(time.Second), ContentTypes: []string{"tool_use"}})
	applyConversationFact(&index, &attention, ConversationEventFact{Role: "user", Timestamp: at.Add(2 * time.Second), ContentTypes: []string{"tool_result"}})
	if index.LastUserPrompt.Timestamp != at || index.LastToolActivity == nil || !index.LastToolActivity.Timestamp.Equal(at.Add(2*time.Second)) {
		t.Fatalf("%+v", index)
	}
	applyConversationFact(&index, &attention, ConversationEventFact{Role: "user", Timestamp: at.Add(3 * time.Second), Terminal: true, Failed: true, StopReason: "interrupted"})
	if inferIndexedConversationTurnState(index) != "failed" {
		t.Fatalf("%+v", index)
	}
}
