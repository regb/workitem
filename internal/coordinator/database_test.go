package coordinator

import (
	"encoding/json"
	"github.com/regb/workitem/internal/model"
	"os"
	"testing"
	"time"

)

func TestDatabaseCommitIsAtomicAndIdempotent(t *testing.T) {
	db, _ := itemDatabase(t)
	revision := uint64(0)
	command := Command{ID: "command-1", ProtocolVersion: 1, Type: "item.created", ItemID: "item-1", ExpectedRevision: &revision, ReceivedAt: time.Now()}
	event := PendingEvent{ID: "event-1", ItemID: "item-1", Type: "item.created", Timestamp: time.Now(), Actor: model.OperatorUser, Payload: json.RawMessage(`{"title":"Test"}`)}
	projection := map[string]any{"id": "item-1", "state": "backlog"}
	encoded, _ := json.Marshal(projection)
	first, err := db.Commit(command, []PendingEvent{event}, []ProjectionUpdate{{Projection: "items", Key: "item-1", Value: encoded}})
	if err != nil {
		t.Fatal(err)
	}
	if first.FinalSequence != 1 || first.ItemRevision != 1 || len(first.Events) != 1 || first.Duplicate {
		t.Fatalf("first result = %+v", first)
	}
	second, err := db.Commit(command, []PendingEvent{event}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.FinalSequence != 1 || second.ItemRevision != 1 {
		t.Fatalf("duplicate result = %+v", second)
	}
	var loaded map[string]any
	ok, err := db.ReadProjection("items", "item-1", &loaded)
	if err != nil || !ok || loaded["state"] != "backlog" {
		t.Fatalf("projection=%v ok=%v err=%v", loaded, ok, err)
	}
	status, err := db.Status()
	if err != nil || status.GlobalSequence != 1 || status.SchemaVersion != SchemaVersion {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestDatabaseRejectsPreReleaseSchema(t *testing.T) {
	if _, err := OpenDatabase(legacySchema(t, 4, nil)); err == nil {
		t.Fatal("older pre-release schema was accepted")
	}
}

func TestDatabaseRejectsRevisionConflictWithoutPartialWrites(t *testing.T) {
	db, _ := itemDatabase(t)
	wrong := uint64(4)
	_, err := db.Commit(Command{ID: "conflict", Type: "item.changed", ItemID: "item-1", ExpectedRevision: &wrong}, []PendingEvent{{ID: "event", ItemID: "item-1", Type: "item.changed"}}, []ProjectionUpdate{{Projection: "items", Key: "item-1", Value: json.RawMessage(`{"bad":true}`)}})
	if err == nil {
		t.Fatal("expected revision conflict")
	}
	status, statusErr := db.Status()
	if statusErr != nil || status.GlobalSequence != 0 {
		t.Fatalf("status=%+v err=%v", status, statusErr)
	}
	var value map[string]any
	if ok, readErr := db.ReadProjection("items", "item-1", &value); readErr != nil || ok {
		t.Fatalf("partial projection ok=%v value=%v err=%v", ok, value, readErr)
	}
}

func TestDatabasePermissions(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	path := db.path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode = %o", info.Mode().Perm())
	}
}
