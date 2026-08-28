package pi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestNativeRPCTransportRetriesLiveEventUntilDaemonAcknowledges(t *testing.T) {
	root, runtimeDir := t.TempDir(), t.TempDir()
	itemID := "01KZYHGDCECSFS4BJ2SNTQP49V"
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := model.NewManifest(itemID, "retry", "Retry", nil, false, now, model.Repository{}, model.Checkout{})
	manifest.State = model.StateWorking
	if err := native.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	seedDB, err := coordinator.OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(manifest)
	digest := sha256.Sum256(encoded)
	if _, err := seedDB.SyncImportedManifests(map[string]coordinator.ImportedManifest{itemID: {Manifest: manifest, Digest: hex.EncodeToString(digest[:])}}, true); err != nil {
		seedDB.Close()
		t.Fatal(err)
	}
	owner, _ := json.Marshal(model.AgentRuntime{ID: "runtime-retry", WorkItemID: itemID, State: model.AgentRuntimeRunning})
	if err := seedDB.UpdateProjections([]coordinator.ProjectionUpdate{{Projection: coordinator.RuntimeOwnershipProjection, Key: itemID, Value: owner}}); err != nil {
		seedDB.Close()
		t.Fatal(err)
	}
	if err := seedDB.Close(); err != nil {
		t.Fatal(err)
	}
	socket, err := coordinator.SocketPath(runtimeDir, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &nativeRPCTransport{spec: ExecSpec{RuntimeID: "runtime-retry", WorkItemID: itemID}, liveClient: &coordinator.Client{SocketPath: socket}, liveQueue: make(chan queuedLiveEvent, 2), liveContext: ctx, liveNonce: "nonce"}
	go transport.runLiveSender(ctx)
	transport.queueLiveEvent(agent.RuntimeEvent{Type: agent.EventTurnStarted, Timestamp: now.Add(time.Second)}, nil)
	time.Sleep(150 * time.Millisecond)
	server, err := coordinator.NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	client := &coordinator.Client{SocketPath: socket}
	deadline := time.Now().Add(3 * time.Second)
	for {
		result, queryErr := client.RuntimeEvents(context.Background(), coordinator.RuntimeEventsRequest{ItemID: itemID, Limit: 10})
		if queryErr == nil && len(result.Events) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued event was not delivered: result=%+v err=%v", result, queryErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := client.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPermanentRuntimeIngestError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "server build rejection", err: &coordinator.ProtocolError{Code: "build_mismatch", Message: "different build"}, want: true},
		{name: "server protocol rejection", err: &coordinator.ProtocolError{Code: "protocol_mismatch", Message: "different protocol"}, want: true},
		{name: "superseded runtime rejection", err: &coordinator.ProtocolError{Code: "runtime_owner_mismatch", Message: "different owner"}, want: true},
		{name: "client build rejection", err: &coordinator.CompatibilityError{Kind: "build"}, want: true},
		{name: "client protocol rejection", err: &coordinator.CompatibilityError{Kind: "protocol"}, want: true},
		{name: "transient connection error", err: context.DeadlineExceeded, want: false},
		{name: "other server rejection", err: &coordinator.ProtocolError{Code: "database_error", Message: "busy"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := permanentRuntimeIngestError(test.err); got != test.want {
				t.Fatalf("permanentRuntimeIngestError(%v) = %v, want %v", test.err, got, test.want)
			}
		})
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
	dir := t.TempDir()
	fakePi := filepath.Join(dir, "fake-pi")
	script := `#!/usr/bin/env bash
set -euo pipefail
while IFS= read -r line; do
  case "$line" in
    *wi-runtime-ready*)
      printf '%s\n' '{"id":"wi-runtime-ready","type":"response","command":"get_state","success":true,"data":{"isStreaming":false,"sessionId":"native-session"}}'
      ;;
    *command-prompt*)
      printf '%s\n' '{"id":"command-prompt","type":"response","command":"prompt","success":true}'
      printf '%s\n' '{"type":"message_start","message":{"role":"user","content":"[wi request request-1]\nprivate prompt"}}'
      printf '%s\n' '{"type":"agent_start"}'
      printf '%s\n' '{"type":"tool_execution_start","toolCallId":"tool-1","toolName":"bash","args":{"command":"private command"}}'
      printf '%s\n' '{"type":"tool_execution_end","toolCallId":"tool-1","toolName":"bash","result":{"content":[{"type":"text","text":"private result"}]},"isError":false}'
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
	daemonSocket, daemonClient := startRuntimeTestDaemon(t, itemID, runtimeID)
	result := make(chan error, 1)
	controlPath := filepath.Join(dir, "control.sock")
	go func() {
		result <- runNativeRPC(context.Background(), fakePi, []string{"--mode", "rpc"}, os.Environ(), ExecSpec{
			CWD: dir, WorkItemID: itemID, RuntimeID: runtimeID,
			ControlSocketPath: controlPath, DaemonSocketPath: daemonSocket, LogPath: filepath.Join(dir, "runtime.log"),
		})
	}()
	waitForDaemonRuntimeEvent(t, daemonClient, itemID, agent.EventRuntimeReady)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := agent.SubmitControlSocket(ctx, controlPath, agent.ControlCommand{ProtocolVersion: agent.RuntimeProtocolVersion, ID: "command-prompt", RuntimeID: runtimeID, WorkItemID: itemID, RequestID: "request-1", Type: agent.CommandPrompt, Message: "[wi request request-1]\nprivate prompt", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
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
