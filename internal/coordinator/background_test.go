package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
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
	if o := observe(true); o.Status != "idle" || o.BackgroundTasks != 1 {
		t.Fatalf("online idle agent: %+v", o)
	}
	// Background work dies with the agent process.
	if o := observe(false); o.BackgroundTasks != 0 {
		t.Fatalf("offline agent: %+v", o)
	}
}
