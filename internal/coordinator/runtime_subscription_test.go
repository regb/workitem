package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestRuntimeEventLongPollWakesAfterCommittedEvent(t *testing.T) {
	root, runtimeDir := t.TempDir(), t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := model.NewManifest(importTestID, "subscription", "Subscription", nil, false, now, model.Repository{}, model.Checkout{})
	manifest.State = model.StateActive
	if err := native.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	if err := seedRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	seedDB, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	seedRuntimeOwner(t, seedDB, importTestID, "runtime-sub")
	if err := seedDB.Close(); err != nil {
		t.Fatal(err)
	}
	socket, _ := SocketPath(runtimeDir, root)
	server, err := NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	client := &Client{SocketPath: socket}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resultCh := make(chan RuntimeEventsResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := client.RuntimeEvents(ctx, RuntimeEventsRequest{ItemID: importTestID, AfterSequence: 0, Limit: 10, WaitMillis: 3000})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()
	time.Sleep(50 * time.Millisecond)
	event := RuntimeSemanticEvent{ID: "runtime-sub:event-1", RuntimeID: "runtime-sub", WorkItemID: importTestID, Type: "turn.started", Timestamp: now.Add(time.Second)}
	if _, err := client.IngestRuntimeEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		t.Fatal(err)
	case result := <-resultCh:
		if len(result.Events) != 1 || result.Events[0].ID != event.ID {
			t.Fatalf("result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("long poll did not wake after runtime event")
	}
	olderBuildClient := &Client{SocketPath: socket, BuildIdentity: "older-build"}
	if _, err := olderBuildClient.IngestRuntimeEvent(ctx, RuntimeSemanticEvent{ID: "runtime-sub:event-2", RuntimeID: "runtime-sub", WorkItemID: importTestID, Type: "agent.settled", Timestamp: now.Add(2 * time.Second)}); err != nil {
		t.Fatalf("current runtime event from older build was rejected: %v", err)
	}
	// Existing runtime hosts predate the schema-control handshake. Compact
	// events still work, but retain the normal runtime-owner fence.
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(time.Second))
	legacyPayload, _ := json.Marshal(RuntimeSemanticEvent{ID: "runtime-sub:event-3", RuntimeID: "runtime-sub", WorkItemID: importTestID, Type: "agent.settled", Timestamp: now.Add(3 * time.Second)})
	if err := json.NewEncoder(conn).Encode(Request{ProtocolVersion: ProtocolVersion, BuildIdentity: "older-build", ID: "legacy-runtime", Method: MethodRuntimeEvent, Payload: legacyPayload}); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	var legacyResponse Response
	err = json.NewDecoder(conn).Decode(&legacyResponse)
	conn.Close()
	if err != nil || !legacyResponse.OK {
		t.Fatalf("legacy runtime response=%+v error=%v", legacyResponse, err)
	}
	_, err = client.IngestRuntimeEvent(ctx, RuntimeSemanticEvent{ID: "stale:event-1", RuntimeID: "stale", WorkItemID: importTestID, Type: "turn.started", Timestamp: now.Add(3 * time.Second)})
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.Code != "runtime_owner_mismatch" {
		t.Fatalf("stale runtime error = %v", err)
	}
	if err := client.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
