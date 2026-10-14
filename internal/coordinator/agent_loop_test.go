package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestLiveAgentLoopDoesNotSettleOnIntermediateFailures(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m := model.NewManifest(importTestID, "loop", "Loop", nil, false, now, model.Repository{}, model.Checkout{})
	m.State = model.StateWorking
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
	seedRuntimeOwner(t, db, importTestID, "owner")
	observer := NewExternalObserver(db, root)
	sequence := 0
	emit := func(typ string, failed bool, want string) {
		t.Helper()
		sequence++
		event := RuntimeSemanticEvent{ID: fmt.Sprintf("loop-%d", sequence), RuntimeID: "owner", WorkItemID: importTestID, Type: typ, Timestamp: now.Add(time.Duration(sequence) * time.Millisecond), Failed: failed, Role: "assistant"}
		if typ == agent.EventAgentSettled {
			event.Outcome = "completed"
			if failed {
				event.Outcome = "failed"
			}
		}
		if _, err := IngestRuntimeEvent(context.Background(), db, event); err != nil {
			t.Fatal(err)
		}
		var observation AgentObservation
		if found, err := db.ReadProjection(AgentObservationProjection, importTestID, &observation); err != nil || !found || observation.Status != want {
			t.Fatalf("event=%s observation=%+v err=%v want=%s", typ, observation, err, want)
		}
	}
	emit(agent.EventAgentStarted, false, "busy")
	emit(agent.EventTurnStarted, false, "busy")
	emit(agent.EventToolCompleted, true, "busy")
	emit(agent.EventMessageCompleted, true, "busy")
	emit(agent.EventTurnEnded, true, "busy")
	var activity RuntimeActivity
	db.ReadProjection(RuntimeActivityProjection, importTestID, &activity)
	if activity.AgentState != "active" || activity.LastCompletedAt != nil {
		t.Fatalf("inner failure settled agent loop: %+v", activity)
	}
	// Native history can contain a newer terminal assistant error while the
	// outer run is retrying. Active ownership must win without any age heuristic.
	at := now.Add(time.Hour)
	index := ConversationIndex{WorkItemID: importTestID, InferredTurnState: "failed", LastTurnActivity: &ConversationEventFact{Timestamp: at, Role: "assistant", Failed: true}}
	raw, _ := json.Marshal(index)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: ConversationProjection, Key: importTestID, Value: raw}}); err != nil {
		t.Fatal(err)
	}
	observation := AgentObservation{WorkItemID: importTestID, ProcessOnline: true}
	if err := observer.applyActionability(importTestID, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Status != "busy" || observation.AgentLoopState != "active" {
		t.Fatalf("history overrode active hooks: %+v", observation)
	}
	// A command refused during active work is not a stopped agent loop.
	emit(agent.EventCommandRejected, true, "busy")
	emit(agent.EventAgentStarted, false, "busy")
	emit(agent.EventMessageCompleted, false, "busy")
	emit(agent.EventTurnEnded, false, "busy")
	emit(agent.EventAgentSettled, false, "idle")
	emit(agent.EventAgentStarted, false, "busy")
	emit(agent.EventMessageCompleted, true, "busy")
	emit(agent.EventTurnEnded, true, "busy")
	emit(agent.EventAgentSettled, true, "problem")
	db.ReadProjection(RuntimeActivityProjection, importTestID, &activity)
	if activity.AgentState != "failed" || activity.TurnState != "failed" {
		t.Fatal(activity)
	}
	emit(agent.EventAgentStarted, false, "busy")
	emit(agent.EventRuntimeFailed, true, "problem")
}

func TestAgentLoopAuthorityRequiresOnlineCurrentOwner(t *testing.T) {
	for _, tt := range []struct {
		name, owner, source, phase string
		online                     bool
		want                       string
	}{
		{"online", "owner", "daemon.runtime.live", "active", true, "busy"},
		{"offline", "owner", "daemon.runtime.live", "active", false, "problem"},
		{"old owner", "old-owner", "daemon.runtime.live", "active", true, "problem"},
		{"file cache", "owner", "legacy.file", "active", true, "problem"},
		{"older driver settlement", "owner", "daemon.runtime.live", "", true, "problem"},
		{"settled failure", "owner", "daemon.runtime.live", "failed", true, "problem"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			db, err := OpenDatabase(root)
			if err != nil {
				t.Fatal(err)
			}
			seedRuntimeOwner(t, db, importTestID, "owner")
			at := time.Now().Add(-time.Hour)
			live := RuntimeActivity{RuntimeID: tt.owner, AgentState: tt.phase, TurnState: "idle", Source: tt.source, LastEventAt: &at}
			index := ConversationIndex{InferredTurnState: "failed", LastTurnActivity: &ConversationEventFact{Timestamp: time.Now(), Role: "assistant", Failed: true}}
			a, _ := json.Marshal(live)
			b, _ := json.Marshal(index)
			if err := db.UpdateProjections([]ProjectionUpdate{{Projection: RuntimeActivityProjection, Key: importTestID, Value: a}, {Projection: ConversationProjection, Key: importTestID, Value: b}}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = OpenDatabase(root)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			observation := AgentObservation{ProcessOnline: tt.online}
			if err := NewExternalObserver(db, root).applyActionability(importTestID, &observation); err != nil {
				t.Fatal(err)
			}
			if observation.Status != tt.want {
				t.Fatalf("observation=%+v want=%s", observation, tt.want)
			}
		})
	}
}
