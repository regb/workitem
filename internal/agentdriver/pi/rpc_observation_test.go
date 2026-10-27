package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
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

// Readers fetch history at the branch tip the live content reports. Pi's
// idle report can arrive while work the host submitted is still held
// active; the tip must still advance, and the finished reply stays shown.
func TestNativeObservationKeepsTheContentBranchCurrent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	content := &driverprotocol.ContentServer{}
	transport := &nativeRPCTransport{spec: ExecSpec{RuntimeID: "owner", WorkItemID: "item"}, content: content, liveContext: ctx, liveNonce: "nonce", liveQueue: make(chan queuedLiveEvent, 8), work: map[string]bool{"submitted": false}}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case queued := <-transport.liveQueue:
				if queued.ack != nil {
					close(queued.ack)
				}
			}
		}
	}()
	content.StartMessage()
	content.Update("message.delta", "Updated rules_python.", "")
	raw, _ := json.Marshal(map[string]any{"type": "wi_runtime_status", "runtime_id": "owner", "work_item_id": "item", "agent_state": "idle", "readiness": "ready", "leaf_id": "093285ce"})
	if err := transport.handleOutput(raw); err != nil {
		t.Fatal(err)
	}
	snapshot := content.Snapshot()
	if snapshot.LeafID != "093285ce" || snapshot.Text != "Updated rules_python." {
		t.Fatalf("branch or reply lost: %+v", snapshot)
	}
}

func TestHeadlessSettingsReportBecomesARuntimeEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &nativeRPCTransport{spec: ExecSpec{RuntimeID: "owner", WorkItemID: "item"}, liveContext: ctx, liveNonce: "nonce", liveQueue: make(chan queuedLiveEvent, 8), work: map[string]bool{}}
	if err := transport.handleOutput([]byte(`{"type":"wi_runtime_settings","runtime_id":"owner","work_item_id":"item","settings":{"model":"openai/gpt","reasoning":"high"}}`)); err != nil {
		t.Fatal(err)
	}
	queued := <-transport.liveQueue
	if queued.event.Type != agent.EventSettingsChanged || queued.event.Settings["model"] != "openai/gpt" || queued.event.Settings["reasoning"] != "high" {
		t.Fatalf("event = %+v", queued.event)
	}
	for _, record := range []string{
		`{"type":"wi_runtime_settings","runtime_id":"other","work_item_id":"item","settings":{"model":"openai/gpt"}}`,
		`{"type":"wi_runtime_settings","runtime_id":"owner","work_item_id":"item","settings":{"reasoning":"extreme"}}`,
		`{"type":"wi_runtime_settings","runtime_id":"owner","work_item_id":"item"}`,
	} {
		if err := transport.handleOutput([]byte(record)); err == nil {
			t.Fatalf("accepted %s", record)
		}
	}
}

func TestPiDefaultsComeFromItsSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	catalog := driverprotocol.ModelCatalog{Models: []driverprotocol.ModelOption{{Value: "openai/gpt"}}}
	if model, reasoning := piDefaults(catalog); model != "" || reasoning != "medium" {
		t.Fatalf("without settings: %q %q", model, reasoning)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"defaultProvider":"openai","defaultModel":"gpt","defaultThinkingLevel":"high"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if model, reasoning := piDefaults(catalog); model != "openai/gpt" || reasoning != "high" {
		t.Fatalf("with settings: %q %q", model, reasoning)
	}
}
