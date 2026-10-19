package pi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/regb/workitem/internal/agent"
)

func TestHeadlessNativeObservationUsesExistingRelayAndPreservesReservations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &nativeRPCTransport{spec: ExecSpec{RuntimeID: "owner", WorkItemID: "item"}, liveContext: ctx, liveNonce: "nonce", liveQueue: make(chan queuedLiveEvent, 8), work: map[string]bool{}}
	delivered := make(chan queuedLiveEvent, 8)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case queued := <-transport.liveQueue:
				delivered <- queued
				if queued.ack != nil {
					close(queued.ack)
				}
			}
		}
	}()
	observe := func(state, readiness, leaf string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"type": "wi_runtime_status", "runtime_id": "owner", "work_item_id": "item", "agent_state": state, "readiness": readiness, "leaf_id": leaf})
		if err := transport.handleOutput(raw); err != nil {
			t.Fatal(err)
		}
	}
	observe("failed", "ready", "aborted")
	event := (<-delivered).event
	if event.Type != agent.EventRuntimeStatus || event.AgentState != "failed" || event.LeafID != "aborted" || !transport.lastAssistantFailed {
		t.Fatal("lost native snapshot", event)
	}
	observe("idle", "ready", "previous-good")
	event = (<-delivered).event
	if event.AgentState != "idle" || event.LeafID != "previous-good" || transport.lastAssistantFailed {
		t.Fatal("headless tree retained failed state", event)
	}
	transport.work["submitted"] = false
	observe("idle", "ready", "before-prompt")
	event = (<-delivered).event
	if event.AgentState != "active" || event.Readiness != "busy" || transport.busy {
		t.Fatal("native snapshot erased handoff reservation", event)
	}
	delete(transport.work, "submitted")
	observe("active", "busy", "manual-compaction")
	<-delivered
	if !transport.busy {
		t.Fatal("native compaction was invisible to idle guards")
	}
	observe("idle", "ready", "after-compaction")
	<-delivered
	if transport.busy {
		t.Fatal("native idle did not release observed busy state")
	}
	for _, raw := range []string{
		`{"type":"wi_runtime_status","runtime_id":"other","work_item_id":"item","agent_state":"idle","leaf_id":"leaf","readiness":"ready"}`,
		`{"type":"wi_runtime_status","runtime_id":"owner","work_item_id":"item","agent_state":"idle","leaf_id":"/private/file","readiness":"ready"}`,
	} {
		if err := transport.handleOutput([]byte(raw)); err == nil {
			t.Fatal("accepted invalid private observation")
		}
	}
	select {
	case <-delivered:
		t.Fatal("invalid private status reached the event relay")
	default:
	}
}
