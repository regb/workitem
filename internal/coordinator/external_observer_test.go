package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	processpkg "github.com/regb/workitem/internal/process"
)

// The observer builds an item's agent observation from its verified runtime,
// its conversation and live runtime activity. The steps share one item.
func TestExternalObserverVerifiesRuntimeAndBuildsAgentProjection(t *testing.T) {
	itemID, now := importTestID, time.Now().UTC()
	manifest := testManifest(itemID, "observer", "Observer", now)
	manifest.PrimaryConversation = &model.Conversation{Driver: "pi", ID: "session-1", Locator: "sessions/pi/session-1.jsonl"}
	db, root := itemDatabase(t, manifest)
	processInfo, err := processpkg.New().Info(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	runtime := model.AgentRuntime{ID: "runtime-test", WorkItemID: itemID, State: model.AgentRuntimeRunning, HostPID: os.Getpid(), HostProcessGroup: processInfo.PGRP, HostStartTime: processInfo.StartTime, StartedAt: now, UpdatedAt: now}
	observer := NewExternalObserver(db, root)
	ctx := context.Background()
	put := func(values map[string]any) {
		t.Helper()
		var updates []ProjectionUpdate
		for projection, value := range values {
			encoded, _ := json.Marshal(value)
			updates = append(updates, ProjectionUpdate{Projection: projection, Key: itemID, Value: encoded})
		}
		if err := db.UpdateProjections(updates); err != nil {
			t.Fatal(err)
		}
	}
	observed := func() AgentObservation {
		t.Helper()
		var projection AgentObservation
		if found, err := db.ReadProjection(AgentObservationProjection, itemID, &projection); err != nil || !found {
			t.Fatalf("found=%v err=%v", found, err)
		}
		return projection
	}
	piIndex := ConversationIndex{WorkItemID: itemID, ConversationID: "session-1", Source: "items/" + itemID + "/sessions/pi/session-1.jsonl", ObservedAt: now, InferredTurnState: "idle", LastTurnActivity: &ConversationEventFact{Timestamp: now.Add(-time.Second), Role: "assistant", Terminal: true}}
	put(map[string]any{ConversationProjection: piIndex, RuntimeOwnershipProjection: runtime})
	var external AgentObservation

	t.Run("a verified runtime and a finished conversation", func(t *testing.T) {
		if err := observer.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		external = observed()
		if external.Status != "idle" || !external.ProcessOnline || external.Worktree == nil || external.Worktree.Status != "absent" || external.WorktreeObservedAt.IsZero() {
			t.Fatalf("projection=%+v", external)
		}
		if err := observer.ReconcileRuntime(ctx); err != nil {
			t.Fatal(err)
		}
		runtimeOnly := observed()
		if runtimeOnly.Worktree == nil || !runtimeOnly.WorktreeObservedAt.Equal(external.WorktreeObservedAt) {
			t.Fatalf("runtime-only reconciliation changed the worktree observation: %+v", runtimeOnly)
		}
		external = runtimeOnly
	})

	promptAt := now.Add(time.Second)
	t.Run("a new prompt makes it busy", func(t *testing.T) {
		piIndex.InferredTurnState = "incomplete"
		piIndex.LastTurnActivity = &ConversationEventFact{Timestamp: promptAt, Role: "user"}
		piIndex.LastUserPrompt = piIndex.LastTurnActivity
		put(map[string]any{ConversationProjection: piIndex, AttentionActivityProjection: AttentionActivity{WorkItemID: itemID, LastRequestedAt: &promptAt, ObservedAt: promptAt, Source: piIndex.Source}})
		if err := observer.ReconcileActionability(ctx); err != nil {
			t.Fatal(err)
		}
		got := observed()
		if got.Status != "busy" || got.Activity.LastRequestedAt == nil || !got.Activity.LastRequestedAt.Equal(promptAt) || got.Worktree == nil || !got.ObservedAt.Equal(external.ObservedAt) {
			t.Fatalf("refreshed projection=%+v", got)
		}
	})

	staleAt := promptAt.Add(2 * time.Second)
	t.Run("a replaced runtime's activity is ignored", func(t *testing.T) {
		piIndex.InferredTurnState = "idle"
		piIndex.LastTurnActivity = &ConversationEventFact{Timestamp: promptAt.Add(time.Second), Role: "assistant", Terminal: true}
		put(map[string]any{ConversationProjection: piIndex, RuntimeActivityProjection: RuntimeActivity{WorkItemID: itemID, RuntimeID: "runtime-old", TurnState: "busy", LastEventAt: &staleAt, LastRequestedAt: &staleAt, ObservedAt: staleAt, Source: "daemon.runtime.live"}})
		if err := observer.ReconcileActionability(ctx); err != nil {
			t.Fatal(err)
		}
		if got := observed(); got.Status != "idle" || got.Reason != "latest assistant answer is terminal" {
			t.Fatalf("stale owner activity remained actionable: %+v", got)
		}
	})

	// Actionability reconciliation must compare live activity with canonical
	// runtime ownership, not the potentially stale owner cached in an earlier
	// agent observation. Otherwise a current settled event can be overwritten
	// by the native session's incomplete fallback after runtime replacement.
	t.Run("canonical ownership, not the cached owner, decides", func(t *testing.T) {
		piIndex.InferredTurnState = "incomplete"
		piIndex.LastTurnActivity = &ConversationEventFact{Timestamp: promptAt, Role: "user"}
		settledAt := staleAt.Add(time.Second)
		cached := observed()
		cached.Runtime = &model.AgentRuntime{ID: "runtime-old", WorkItemID: itemID}
		cached.Status = "busy"
		cached.Reason = "latest conversation activity is incomplete and runtime ownership is online"
		put(map[string]any{
			ConversationProjection:     piIndex,
			RuntimeActivityProjection:  RuntimeActivity{WorkItemID: itemID, RuntimeID: runtime.ID, TurnState: "idle", LastEventAt: &settledAt, LastCompletedAt: &settledAt, ObservedAt: settledAt, Source: "daemon.runtime.live"},
			AgentObservationProjection: cached,
		})
		if err := observer.ReconcileActionability(ctx); err != nil {
			t.Fatal(err)
		}
		if got := observed(); got.Status != "idle" || got.Reason != "live runtime event reports settled work" || got.Runtime == nil || got.Runtime.ID != runtime.ID {
			t.Fatalf("current settled activity was not restored from canonical ownership: %+v", got)
		}
	})
}
