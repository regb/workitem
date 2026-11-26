package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/testutil"
)

func runtimeTestServer(t *testing.T, now time.Time) *Server {
	t.Helper()
	root := t.TempDir()
	seedItems(t, root, testManifest(importTestID, "subscription", "Subscription", now))
	server, err := NewServer(root, filepath.Join(testutil.ShortTempDir(t), "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	seedRuntimeOwner(t, server.database, importTestID, "runtime-sub")
	return server
}

// A poll after the current sequence waits for the next event and returns it
// well before its wait ends.
func TestRuntimeEventLongPollWakesOnTheNextEvent(t *testing.T) {
	now := time.Now().UTC()
	client := runtimeTestServer(t, now).LocalClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ingest := func(id string, at time.Time) {
		t.Helper()
		if _, err := client.IngestRuntimeEvent(ctx, RuntimeSemanticEvent{ID: id, RuntimeID: "runtime-sub", WorkItemID: importTestID, Type: "turn.started", Timestamp: at}); err != nil {
			t.Fatal(err)
		}
	}
	ingest("runtime-sub:event-1", now)
	current, err := client.RuntimeEvents(ctx, RuntimeEventsRequest{ItemID: importTestID, Limit: 10})
	if err != nil || len(current.Events) != 1 {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	type outcome struct {
		result RuntimeEventsResult
		err    error
	}
	polled := make(chan outcome, 1)
	go func() {
		result, err := client.RuntimeEvents(ctx, RuntimeEventsRequest{ItemID: importTestID, AfterSequence: current.LastSequence, Limit: 10, WaitMillis: 30000})
		polled <- outcome{result, err}
	}()
	ingest("runtime-sub:event-2", now.Add(time.Second))
	select {
	case got := <-polled:
		if got.err != nil || len(got.result.Events) != 1 || got.result.Events[0].Sequence <= current.LastSequence {
			t.Fatalf("result=%+v err=%v", got.result, got.err)
		}
	case <-ctx.Done():
		t.Fatal("long poll did not wake on the next runtime event")
	}
}

// Runtime events from a host keep the runtime-owner fence whatever its build;
// only a persistence failure is worth retrying.
func TestRuntimeHostEventsAreFencedAndRetryable(t *testing.T) {
	now := time.Now().UTC()
	server := runtimeTestServer(t, now)
	ctx := context.Background()
	send := func(build string, schema uint64, event RuntimeSemanticEvent) error {
		payload, _ := json.Marshal(event)
		_, err := server.dispatch(ctx, Request{ProtocolVersion: ProtocolVersion, SchemaVersion: schema, BuildIdentity: build, ID: event.ID, Method: MethodRuntimeEvent, Payload: payload}, AccessRuntimeHost)
		return err
	}
	event := func(id, runtime, typ string, offset time.Duration) RuntimeSemanticEvent {
		return RuntimeSemanticEvent{ID: id, RuntimeID: runtime, WorkItemID: importTestID, Type: typ, Timestamp: now.Add(offset)}
	}
	// Hosts of an older build, or predating the schema handshake, still report.
	if err := send("older-build", SchemaVersion, event("runtime-sub:event-1", "runtime-sub", "turn.started", time.Second)); err != nil {
		t.Fatalf("runtime event from an older build was rejected: %v", err)
	}
	if err := send("older-build", 0, event("runtime-sub:event-2", "runtime-sub", "agent.settled", 2*time.Second)); err != nil {
		t.Fatalf("runtime event from a host without schema was rejected: %v", err)
	}
	code := func(err error) string {
		var protocolErr *ProtocolError
		if !errors.As(err, &protocolErr) {
			return ""
		}
		return protocolErr.Code
	}
	if err := send("", SchemaVersion, event("stale:event-1", "stale", "turn.started", 3*time.Second)); code(err) != "runtime_owner_mismatch" {
		t.Fatalf("stale runtime error = %v", err)
	}
	if err := send("", SchemaVersion, event("runtime-sub:invalid", "runtime-sub", "unknown", 0)); code(err) != "runtime_event_error" {
		t.Fatalf("invalid event error = %v", err)
	}
	// Validation and ownership rejection are permanent. Persistence/read errors
	// must not tell a live producer to disable its event feed.
	var saved RuntimeActivity
	if _, err := server.database.ReadProjection(RuntimeActivityProjection, importTestID, &saved); err != nil {
		t.Fatal(err)
	}
	if err := server.database.UpdateProjections([]ProjectionUpdate{{Projection: RuntimeActivityProjection, Key: importTestID, Value: []byte("{broken")}}); err != nil {
		t.Fatal(err)
	}
	retry := event("runtime-sub:retry", "runtime-sub", "agent.settled", 4*time.Second)
	if err := send("", SchemaVersion, retry); code(err) != "database_error" {
		t.Fatalf("database failure became definitive rejection: %v", err)
	}
	restored, _ := json.Marshal(saved)
	if err := server.database.UpdateProjections([]ProjectionUpdate{{Projection: RuntimeActivityProjection, Key: importTestID, Value: restored}}); err != nil {
		t.Fatal(err)
	}
	if err := send("", SchemaVersion, retry); err != nil {
		t.Fatalf("repaired event could not be retried: %v", err)
	}
}
