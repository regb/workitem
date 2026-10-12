package pi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestNativeRPCTransportRetriesLiveEventUntilEventSocketAcknowledges(t *testing.T) {
	dir := testutil.ShortTempDir(t)
	socket := filepath.Join(dir, "events.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &nativeRPCTransport{spec: ExecSpec{RuntimeID: "runtime-retry", WorkItemID: "item-retry", EventSocketPath: socket}, liveQueue: make(chan queuedLiveEvent, 2), liveContext: ctx, liveNonce: "nonce"}
	go transport.runLiveSender(ctx)
	transport.queueLiveEvent(agent.RuntimeEvent{Type: agent.EventTurnStarted, Timestamp: time.Now()}, nil)
	time.Sleep(150 * time.Millisecond)
	server, err := driverprotocol.ListenEvents(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	select {
	case request := <-server.Requests():
		if request.Event.Type != agent.EventTurnStarted || request.Event.RuntimeID != "runtime-retry" {
			t.Fatalf("event=%+v", request.Event)
		}
		request.Respond(nil)
	case <-time.After(3 * time.Second):
		t.Fatal("queued event was not delivered")
	}
}

func TestNativeRPCTransportStopsAfterEventRejection(t *testing.T) {
	dir := testutil.ShortTempDir(t)
	socket := filepath.Join(dir, "events.sock")
	server, err := driverprotocol.ListenEvents(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &nativeRPCTransport{spec: ExecSpec{RuntimeID: "runtime-rejected", WorkItemID: "item-rejected", EventSocketPath: socket}, liveQueue: make(chan queuedLiveEvent, 2), liveContext: ctx, liveNonce: "nonce"}
	go transport.runLiveSender(ctx)
	transport.queueLiveEvent(agent.RuntimeEvent{Type: agent.EventTurnStarted, Timestamp: time.Now()}, nil)
	request := <-server.Requests()
	request.Respond(errors.New("runtime owner changed"))
	deadline := time.Now().Add(time.Second)
	for !transport.liveDisabled.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !transport.liveDisabled.Load() {
		t.Fatal("event sender continued after explicit rejection")
	}
}

func TestNativeRPCTransportRejectsMismatchedControlIdentity(t *testing.T) {
	transport := &nativeRPCTransport{spec: ExecSpec{RuntimeID: "runtime-1", WorkItemID: "item-1"}, handled: map[string]bool{}, pending: map[string]agent.ControlCommand{}}
	for _, command := range []agent.ControlCommand{
		{ProtocolVersion: agent.RuntimeProtocolVersion + 1, ID: "bad-version", RuntimeID: "runtime-1", WorkItemID: "item-1", Type: agent.CommandAbort},
		{ProtocolVersion: agent.RuntimeProtocolVersion, ID: "bad-runtime", RuntimeID: "runtime-2", WorkItemID: "item-1", Type: agent.CommandAbort},
		{ProtocolVersion: agent.RuntimeProtocolVersion, ID: "bad-item", RuntimeID: "runtime-1", WorkItemID: "item-2", Type: agent.CommandAbort},
	} {
		if err := transport.sendCommand(command); err == nil {
			t.Fatalf("command %+v was accepted", command)
		}
	}
}

func TestNativeRPCTransportControlsAndPublishesOnlyCompactEvents(t *testing.T) {
	dir := testutil.ShortTempDir(t)
	fakePi := filepath.Join(dir, "fake-pi")
	script := `#!/usr/bin/env bash
set -euo pipefail
while IFS= read -r line; do
  case "$line" in
    *wi-runtime-ready*)
      sleep 0.2
      printf '%s\n' '{"id":"wi-runtime-ready","type":"response","command":"get_commands","success":true,"data":{"commands":[{"name":"wi-control-idle"}]}}'
      ;;
    *command-prompt*)
      printf '%s\n' '{"id":"command-prompt","type":"response","command":"prompt","success":true}'
      printf '%s\n' '{"type":"message_start","message":{"role":"user","content":"[wi request request-1]\nprivate prompt"}}'
      printf '%s\n' '{"type":"agent_start"}'
      printf '%s\n' '{"type":"tool_execution_start","toolCallId":"tool-1","toolName":"bash","args":{"command":"private command"}}'
      printf '%s\n' '{"type":"tool_execution_end","toolCallId":"tool-1","toolName":"bash","result":{"content":[{"type":"text","text":"private result"}]},"isError":false}'
      printf '%s\n' '{"type":"message_start","message":{"role":"assistant","content":[]}}'
      printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"live private text"}}'
      sleep 1
      printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"private answer"}],"stopReason":"stop"}}'
      printf '%s\n' '{"type":"agent_settled"}'
      ;;
  esac
done
`
	if err := os.WriteFile(fakePi, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	itemID, runtimeID := "01K00000000000000000000000", "rpc-test"
	_, daemonClient := startRuntimeTestDaemon(t, itemID, runtimeID)
	result := make(chan error, 1)
	controlPath := filepath.Join(dir, "control.sock")
	eventPath := startRuntimeTestEventRelay(t, filepath.Join(dir, "events.sock"), daemonClient)
	go func() {
		result <- runNativeRPC(context.Background(), fakePi, []string{"--mode", "rpc"}, os.Environ(), ExecSpec{
			CWD: dir, WorkItemID: itemID, RuntimeID: runtimeID,
			ControlSocketPath: controlPath, EventSocketPath: eventPath, LogPath: filepath.Join(dir, "runtime.log"),
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(controlPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := agent.SubmitControlSocket(ctx, controlPath, agent.ControlCommand{ProtocolVersion: agent.RuntimeProtocolVersion, ID: "command-prompt", RuntimeID: runtimeID, WorkItemID: itemID, RequestID: "request-1", Type: agent.CommandPrompt, Message: "[wi request request-1]\nprivate prompt", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	waitForDaemonRuntimeEvent(t, daemonClient, itemID, agent.EventRuntimeReady)
	contentSeen := false
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		value, readErr := driverprotocol.ReadContent(ctx, driverprotocol.ContentSocketPath(controlPath), runtimeID, itemID)
		if readErr == nil && value.Text == "live private text" {
			contentSeen = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !contentSeen {
		t.Fatal("live text was not available through public content endpoint")
	}
	events := waitForDaemonRuntimeEvent(t, daemonClient, itemID, agent.EventAgentSettled)
	types := map[string]bool{}
	for _, event := range events {
		types[event.Type] = true
		if strings.Contains(string(event.Payload), "private") {
			t.Fatalf("unrestricted payload entered daemon event: %s", event.Payload)
		}
	}
	for _, eventType := range []string{agent.EventCommandAccepted, agent.EventToolStarted, agent.EventToolCompleted, agent.EventMessageCompleted, agent.EventAgentSettled} {
		if !types[eventType] {
			t.Fatalf("event %s missing from %+v", eventType, types)
		}
	}
	if err := agent.SubmitControlSocket(ctx, controlPath, agent.ControlCommand{ProtocolVersion: agent.RuntimeProtocolVersion, ID: "command-shutdown", RuntimeID: runtimeID, WorkItemID: itemID, Type: agent.CommandShutdown, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native RPC transport did not shut down")
	}
}

func startRuntimeTestEventRelay(t *testing.T, path string, client *coordinator.Client) string {
	t.Helper()
	server, err := driverprotocol.ListenEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for request := range server.Requests() {
			event := request.Event
			_, err := client.IngestRuntimeEvent(context.Background(), coordinator.RuntimeSemanticEvent{ID: event.EventID, RuntimeID: event.RuntimeID, WorkItemID: event.WorkItemID, Type: event.Type, Timestamp: event.Timestamp, CommandID: event.CommandID, RequestID: event.RequestID, Role: event.Role, ToolName: event.ToolName, Failed: event.Failed})
			request.Respond(err)
		}
	}()
	t.Cleanup(func() { _ = server.Close() })
	return path
}

func startRuntimeTestDaemon(t *testing.T, itemID, runtimeID string) (string, *coordinator.Client) {
	t.Helper()
	root, runtimeDir := t.TempDir(), t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := model.NewManifest(itemID, "rpc", "RPC", nil, false, now, model.Repository{}, model.Checkout{})
	manifest.State = model.StateWorking
	if err := native.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	db, err := coordinator.OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(manifest)
	digest := sha256.Sum256(encoded)
	if _, err := db.SyncImportedManifests(map[string]coordinator.ImportedManifest{itemID: {Manifest: manifest, Digest: hex.EncodeToString(digest[:])}}, true); err != nil {
		db.Close()
		t.Fatal(err)
	}
	owner, _ := json.Marshal(model.AgentRuntime{ID: runtimeID, WorkItemID: itemID, State: model.AgentRuntimeRunning})
	if err := db.UpdateProjections([]coordinator.ProjectionUpdate{{Projection: coordinator.RuntimeOwnershipProjection, Key: itemID, Value: owner}}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	socket, err := coordinator.SocketPath(runtimeDir, root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := coordinator.NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	client := &coordinator.Client{SocketPath: socket}
	t.Cleanup(func() {
		_ = client.Shutdown(context.Background())
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("runtime test daemon did not stop")
		}
	})
	return socket, client
}

func waitForDaemonRuntimeEvent(t *testing.T, client *coordinator.Client, itemID, eventType string) []coordinator.DomainEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err := client.RuntimeEvents(context.Background(), coordinator.RuntimeEventsRequest{ItemID: itemID, Limit: 100})
		if err == nil {
			for _, event := range result.Events {
				if event.Type == eventType {
					return result.Events
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("event %s was not observed", eventType)
	return nil
}
