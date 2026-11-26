package pi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/testutil"
)

// Neither an unreachable event socket nor a retryable relay rejection loses an
// event or changes its identity; the sender retries until one is acknowledged.
func TestNativeRPCTransportRetriesRejectedRelayWithoutLosingSettlement(t *testing.T) {
	root := testutil.ShortTempDir(t)
	socket := filepath.Join(root, "events.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transport := &nativeRPCTransport{spec: ExecSpec{RuntimeID: "retry-owner", WorkItemID: "item", EventSocketPath: socket}, liveQueue: make(chan queuedLiveEvent, 4), liveContext: ctx, liveNonce: "nonce"}
	go transport.runLiveSender(ctx)
	transport.queueLiveEvent(agent.RuntimeEvent{Type: agent.EventAgentStarted, Timestamp: time.Now()}, nil)
	// The first delivery reaches a socket that drops the connection unanswered.
	dropping, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dropping.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	_ = dropping.Close()
	server, err := driverprotocol.ListenEvents(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	select {
	case first := <-server.Requests():
		if first.Event.Type != agent.EventAgentStarted || first.Event.RuntimeID != "retry-owner" {
			t.Fatalf("event=%+v", first.Event)
		}
		first.Respond(nil)
	case <-ctx.Done():
		t.Fatal("queued event was not delivered once the event socket listened")
	}
	settled := agent.RuntimeEvent{Type: agent.EventAgentSettled, Outcome: "completed", Timestamp: time.Now()}
	ack := make(chan struct{})
	transport.queueLiveEvent(settled, ack)
	var id string
	for attempt := 0; attempt < 2; attempt++ {
		select {
		case request := <-server.Requests():
			if id != "" && request.Event.EventID != id {
				t.Fatal("retry changed event identity")
			}
			id = request.Event.EventID
			request.Respond(&driverprotocol.EventRejectedError{Message: "coordinator unavailable", Retryable: true})
		case <-ctx.Done():
			t.Fatal("retryable relay rejection stopped delivery")
		}
	}
	select {
	case request := <-server.Requests():
		if request.Event.EventID != id || request.Event.Type != agent.EventAgentSettled || request.Event.Outcome != "completed" {
			t.Fatal("settlement changed during retry")
		}
		request.Respond(nil)
	case <-ctx.Done():
		t.Fatal("settlement lost")
	}
	select {
	case <-ack:
	case <-ctx.Done():
		t.Fatal("delivery acknowledgement lost")
	}
	if transport.liveDisabled.Load() {
		t.Fatal("temporary failure disabled the event feed")
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
	script := `set -eu
while IFS= read -r line; do
  case "$line" in
    *wi-runtime-ready*)
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
      while [ ! -f "$1" ]; do sleep 0.01; done
      printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"private answer"}],"stopReason":"stop"}}'
      printf '%s\n' '{"type":"agent_settled"}'
      ;;
  esac
done
`
	testutil.WriteScript(t, fakePi, script)
	itemID, runtimeID := "01K00000000000000000000000", "rpc-test"
	// The fake Pi saves its answer once the live text has been read.
	answer := filepath.Join(dir, "answer")
	result := make(chan error, 1)
	controlPath := filepath.Join(dir, "control.sock")
	events := recordEvents(t, filepath.Join(dir, "events.sock"))
	go func() {
		result <- runNativeRPC(context.Background(), fakePi, []string{answer}, os.Environ(), ExecSpec{
			CWD: dir, WorkItemID: itemID, RuntimeID: runtimeID,
			ControlSocketPath: controlPath, EventSocketPath: events.path, LogPath: filepath.Join(dir, "runtime.log"),
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
	events.waitFor(t, agent.EventRuntimeReady)
	contentSeen := false
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		value, readErr := driverprotocol.ReadContent(ctx, driverprotocol.ContentSocketPath(controlPath), runtimeID, itemID)
		if readErr == nil && value.Text() == "live private text" {
			contentSeen = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !contentSeen {
		t.Fatal("live text was not available through public content endpoint")
	}
	if err := os.WriteFile(answer, nil, 0600); err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, event := range events.waitFor(t, agent.EventAgentSettled) {
		types[event.Type] = true
		if encoded, _ := json.Marshal(event); strings.Contains(string(encoded), "private") {
			t.Fatalf("unrestricted payload entered runtime event: %s", encoded)
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

// eventRecorder accepts every runtime event sent to its socket.
type eventRecorder struct {
	path     string
	received chan driverprotocol.RuntimeEvent
	seen     []driverprotocol.RuntimeEvent
}

func recordEvents(t *testing.T, path string) *eventRecorder {
	t.Helper()
	server, err := driverprotocol.ListenEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &eventRecorder{path: path, received: make(chan driverprotocol.RuntimeEvent, 256)}
	go func() {
		for request := range server.Requests() {
			recorder.received <- request.Event
			request.Respond(nil)
		}
	}()
	t.Cleanup(func() { _ = server.Close() })
	return recorder
}

// waitFor returns every event received so far once one of eventType arrives.
func (r *eventRecorder) waitFor(t *testing.T, eventType string) []driverprotocol.RuntimeEvent {
	t.Helper()
	for _, event := range r.seen {
		if event.Type == eventType {
			return r.seen
		}
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-r.received:
			r.seen = append(r.seen, event)
			if event.Type == eventType {
				return r.seen
			}
		case <-timeout:
			t.Fatalf("event %s was not observed in %+v", eventType, r.seen)
		}
	}
}

// drain returns every event received so far without waiting.
func (r *eventRecorder) drain() []driverprotocol.RuntimeEvent {
	for {
		select {
		case event := <-r.received:
			r.seen = append(r.seen, event)
		default:
			return r.seen
		}
	}
}

// An abort starts no work, so the transport publishes no early acceptance
// for it: only Pi's response, marked as an accepted abort. An unmarked one
// would leave an agent aborted while idle reported busy.
func TestNativeRPCTransportAbortIsNotPublishedAsWork(t *testing.T) {
	dir := testutil.ShortTempDir(t)
	fakePi := filepath.Join(dir, "fake-pi")
	script := `set -eu
while IFS= read -r line; do
  case "$line" in
    *wi-runtime-ready*)
      printf '%s\n' '{"id":"wi-runtime-ready","type":"response","command":"get_commands","success":true,"data":{"commands":[{"name":"wi-control-idle"}]}}'
      ;;
    *command-abort*)
      printf '%s\n' '{"id":"command-abort","type":"response","command":"abort","success":true}'
      ;;
  esac
done
`
	testutil.WriteScript(t, fakePi, script)
	itemID, runtimeID := "01K00000000000000000000000", "rpc-abort"
	controlPath := filepath.Join(dir, "control.sock")
	events := recordEvents(t, filepath.Join(dir, "events.sock"))
	result := make(chan error, 1)
	go func() {
		result <- runNativeRPC(context.Background(), fakePi, []string{"--mode", "rpc"}, os.Environ(), ExecSpec{
			CWD: dir, WorkItemID: itemID, RuntimeID: runtimeID,
			ControlSocketPath: controlPath, EventSocketPath: events.path, LogPath: filepath.Join(dir, "runtime.log"),
		})
	}()
	events.waitFor(t, agent.EventRuntimeReady)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := agent.SubmitControlSocket(ctx, controlPath, agent.ControlCommand{ProtocolVersion: agent.RuntimeProtocolVersion, ID: "command-abort", RuntimeID: runtimeID, WorkItemID: itemID, Type: agent.CommandAbort, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	events.waitFor(t, agent.EventCommandAccepted)
	// Every event the runtime publishes is delivered before it exits.
	if err := agent.SubmitControlSocket(ctx, controlPath, agent.ControlCommand{ProtocolVersion: agent.RuntimeProtocolVersion, ID: "command-shutdown", RuntimeID: runtimeID, WorkItemID: itemID, Type: agent.CommandShutdown, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("native RPC transport did not shut down")
	}
	for _, event := range events.drain() {
		if event.Type == agent.EventCommandAccepted && event.CommandID == "command-abort" && event.ReasonCode != agent.ReasonAbortAccepted {
			t.Fatalf("abort published as work: %+v", event)
		}
	}
}
