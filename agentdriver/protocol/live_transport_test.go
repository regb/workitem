package driverprotocol

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControlTransport(t *testing.T) {
	server, err := ListenControl(filepath.Join(t.TempDir(), "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	command := ControlCommand{ProtocolVersion: RuntimeVersion, ID: "command-1", RuntimeID: "runtime-1", WorkItemID: "item-1", Type: CommandPrompt, Message: "test", CreatedAt: time.Now()}
	done := make(chan error, 1)
	go func() {
		done <- SubmitControl(context.Background(), filepath.Join(filepath.Dir(server.path), "control.sock"), command)
	}()
	request := <-server.Requests()
	if request.Command.ID != command.ID {
		t.Fatalf("command=%+v", request.Command)
	}
	request.Respond(nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestControlServerCloseWaitsForActiveHandlers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server, err := ListenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	command := ControlCommand{ProtocolVersion: RuntimeVersion, ID: "command-1", RuntimeID: "runtime-1", WorkItemID: "item-1", Type: CommandAbort, CreatedAt: time.Now()}
	submitDone := make(chan error, 1)
	go func() { submitDone <- SubmitControl(context.Background(), path, command) }()
	<-server.Requests()
	closeDone := make(chan error, 1)
	go func() { closeDone <- server.Close() }()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("control server close waited indefinitely for handler")
	}
	select {
	case <-submitDone:
	case <-time.After(time.Second):
		t.Fatal("control client remained blocked after server close")
	}
}

func TestEventServerCloseInterruptsPartialRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.sock")
	server, err := ListenEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"protocol_version":1`)); err != nil {
		t.Fatal(err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- server.Close() }()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("event server close did not interrupt partial request")
	}
}

func TestEventTransportReportsRetryableRelayFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.sock")
	server, err := ListenEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- SubmitEvent(context.Background(), path, RuntimeEvent{EventID: "event-1"}) }()
	request := <-server.Requests()
	request.Respond(&EventRejectedError{Message: "coordinator temporarily unavailable", Retryable: true})
	var rejected *EventRejectedError
	if err := <-done; !errors.As(err, &rejected) || !rejected.Retryable {
		t.Fatal("transport lost retryable classification", err)
	}
}

func TestEventTransportReportsRejection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.sock")
	server, err := ListenEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	event := RuntimeEvent{ProtocolVersion: RuntimeVersion, EventID: "event-1", RuntimeID: "runtime-1", WorkItemID: "item-1", Type: EventRuntimeReady, Timestamp: time.Now()}
	done := make(chan error, 1)
	go func() { done <- SubmitEvent(context.Background(), path, event) }()
	request := <-server.Requests()
	if request.Event.EventID != event.EventID {
		t.Fatalf("event=%+v", request.Event)
	}
	request.Respond(errors.New("wrong owner"))
	if err := <-done; err == nil || err.Error() != "wrong owner" {
		t.Fatalf("err=%v", err)
	}
}

// Clients treat a runtime socket's path as readiness: once it exists, a dial
// must reach a listening socket rather than one that is only bound.
func TestRuntimeSocketPathAppearsOnlyWhenListening(t *testing.T) {
	dir := t.TempDir()
	for attempt := 0; attempt < 200; attempt++ {
		path := filepath.Join(dir, "control.sock")
		dialed := make(chan error, 1)
		go func() {
			for {
				if _, err := os.Stat(path); err == nil {
					conn, err := net.Dial("unix", path)
					if err == nil {
						_ = conn.Close()
					}
					dialed <- err
					return
				}
			}
		}()
		server, err := ListenControl(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := <-dialed; err != nil {
			t.Fatalf("attempt %d: socket path existed before it accepted connections: %v", attempt, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
			t.Fatalf("socket mode = %v", info.Mode())
		}
		_ = server.Close()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("closed socket remains: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("pending socket names left behind: %v", entries)
	}
}

func TestRuntimeSocketReplacesStalePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.sock")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := ListenEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}
