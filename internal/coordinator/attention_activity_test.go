package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	bolt "go.etcd.io/bbolt"
)

func attentionTestDatabase(t *testing.T) (*Database, string) {
	t.Helper()
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	manifest := model.NewManifest(importTestID, "attention", "Attention", nil, false, time.Now().UTC(), model.Repository{}, model.Checkout{})
	if _, err := db.SyncImportedManifests(map[string]ImportedManifest{importTestID: {Manifest: manifest, Digest: "fixture"}}, true); err != nil {
		t.Fatal(err)
	}
	return db, root
}
func attentionEventCommand(t *testing.T, db *Database, kind string, at time.Time) (StoreCommand, json.RawMessage) {
	t.Helper()
	canonical, err := db.CanonicalManifest(importTestID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(model.NewEvent(at, kind, "user", map[string]any{"at": at}))
	if err != nil {
		t.Fatal(err)
	}
	command := StoreCommand{ID: fmt.Sprintf("%s:%d", kind, at.UnixNano()), ProtocolVersion: ProtocolVersion, Operation: StoreEventAppend, ItemID: importTestID, ExpectedRevision: &canonical.Revision, PayloadDigest: StorePayloadDigest(payload), CreatedAt: at}
	if err := StageStoreMutation(db, command, payload); err != nil {
		t.Fatal(err)
	}
	return command, payload
}
func appendAttentionEvent(t *testing.T, db *Database, kind string, at time.Time) {
	t.Helper()
	command, payload := attentionEventCommand(t, db, kind, at)
	if _, err := db.ExecuteStoreCommand(command, payload); err != nil {
		t.Fatal(err)
	}
}

func TestAttentionSnapshotIsCompactAndIndependentOfHistory(t *testing.T) {
	db, root := attentionTestDatabase(t)
	fresh, err := db.AttentionActivity(importTestID)
	if err != nil || fresh.Found || fresh.Activity.WorkItemID != importTestID {
		t.Fatal(fresh, err)
	}
	now := time.Now().UTC()
	requested, completed := now.Add(-3*time.Minute), now.Add(-2*time.Minute)
	base := AttentionActivity{WorkItemID: importTestID, LastRequestedAt: &requested, LastCompletedAt: &completed}
	data, _ := json.Marshal(base)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AttentionActivityProjection, Key: importTestID, Value: data}}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"attention.read", "attention.unread", "attention.deferred"} {
		appendAttentionEvent(t, db, kind, now)
		appendAttentionEvent(t, db, kind, now.Add(-time.Minute))
	}
	// History is deliberately unreadable. The snapshot endpoint must not scan it.
	if err := db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketEvents).Put(encodeUint64(999999), []byte("unreadable event history"))
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.AttentionActivity(importTestID)
	if err != nil || !snapshot.Found || !snapshot.Projection.Fresh {
		t.Fatal(snapshot, err)
	}
	activity := snapshot.Activity
	for _, at := range []*time.Time{activity.LastReadAt, activity.LastUnreadAt, activity.LastDeferredAt} {
		if at == nil || !at.Equal(now) {
			t.Fatal("marker moved backwards", activity)
		}
	}
	if !activity.LastRequestedAt.Equal(requested) || !activity.LastCompletedAt.Equal(completed) {
		t.Fatal("marker write erased native facts")
	}
	encoded, _ := json.Marshal(snapshot)
	if len(encoded) > 2048 {
		t.Fatal("attention snapshot grew with event history")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	durable, err := reopened.AttentionActivity(importTestID)
	if err != nil || !reflect.DeepEqual(durable.Activity, activity) {
		t.Fatal("snapshot was not durable", err)
	}
	if _, err := reopened.AttentionActivity("../escape"); err == nil {
		t.Fatal("invalid item id accepted")
	}
	if _, err := reopened.AttentionActivity("01KZYHGDCECSFS4BJ2SNTQP49W"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("unknown item treated as an empty projection", err)
	}
}

func TestNativeAttentionCheckpointCannotOverwriteNewerMarkers(t *testing.T) {
	db, _ := attentionTestDatabase(t)
	old := time.Now().UTC().Add(-time.Hour)
	latest := old.Add(time.Minute)
	for _, kind := range []string{"attention.read", "attention.unread", "attention.deferred"} {
		appendAttentionEvent(t, db, kind, latest)
	}
	// Simulate the snapshot held by a native import that began before the writes.
	stale := AttentionActivity{WorkItemID: importTestID, LastRequestedAt: &old, LastCompletedAt: &old, LastReadAt: &old, LastUnreadAt: &old, LastDeferredAt: &old}
	data, _ := json.Marshal(stale)
	checkpoint := SourceCheckpoint{Source: "attention-checkpoint", Offset: 1, Size: 1}
	if _, err := db.CommitSourceBatch(checkpoint, nil, []ProjectionUpdate{{Projection: AttentionActivityProjection, Key: importTestID, Value: data}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.AttentionActivity(importTestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []*time.Time{snapshot.Activity.LastReadAt, snapshot.Activity.LastUnreadAt, snapshot.Activity.LastDeferredAt} {
		if at == nil || !at.Equal(latest) {
			t.Fatal("native import erased operator intent", snapshot.Activity)
		}
	}
	if snapshot.Activity.LastRequestedAt == nil || !snapshot.Activity.LastRequestedAt.Equal(old) {
		t.Fatal("checkpoint lost native facts")
	}
}

func TestMalformedAttentionProjectionDoesNotFallbackOrCommitMarker(t *testing.T) {
	for _, raw := range []string{"{", "null", `{"work_item_id":"wrong"}`} {
		t.Run(raw, func(t *testing.T) {
			db, _ := attentionTestDatabase(t)
			if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AttentionActivityProjection, Key: importTestID, Value: []byte(raw)}}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.AttentionActivity(importTestID); err == nil {
				t.Fatal("malformed projection became empty activity")
			}
			before, _ := db.Status()
			canonical, _ := db.CanonicalManifest(importTestID)
			command, payload := attentionEventCommand(t, db, "attention.read", time.Now().UTC())
			if _, err := db.ExecuteStoreCommand(command, payload); err == nil {
				t.Fatal("marker overwrote malformed projection")
			}
			after, _ := db.Status()
			retained, _ := db.CanonicalManifest(importTestID)
			if before.GlobalSequence != after.GlobalSequence || canonical.Revision != retained.Revision {
				t.Fatal("failed marker partially committed")
			}
			data, _ := json.Marshal(AttentionActivity{WorkItemID: importTestID})
			checkpoint := SourceCheckpoint{Source: "malformed-attention", Offset: 1, Size: 1}
			if _, err := db.CommitSourceBatch(checkpoint, nil, []ProjectionUpdate{{Projection: AttentionActivityProjection, Key: importTestID, Value: data}}); err == nil {
				t.Fatal("native import silently replaced malformed intent")
			}
			if _, found, err := db.SourceCheckpoint(checkpoint.Source); err != nil || found {
				t.Fatal("failed import advanced its checkpoint", err)
			}
		})
	}
}

func TestAttentionActivityTransportAndAccessBoundary(t *testing.T) {
	db, root := attentionTestDatabase(t)
	at := time.Now().UTC()
	appendAttentionEvent(t, db, "attention.read", at)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	server, err := NewServerWithAgentSocket(root, socket, filepath.Join(filepath.Dir(socket), "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := &Client{SocketPath: socket}
	snapshot, err := client.AttentionActivity(ctx, importTestID)
	if err != nil || snapshot.Activity.LastReadAt == nil || !snapshot.Activity.LastReadAt.Equal(at) {
		t.Fatal(snapshot, err)
	}
	if err := client.Call(ctx, MethodAttentionActivity, map[string]any{"item_id": "../escape"}, nil); err == nil {
		t.Fatal("transport accepted invalid identity")
	}
	restricted := &Client{SocketPath: server.agentSocketPath}
	if _, err := restricted.AttentionActivity(ctx, importTestID); err == nil || !strings.Contains(err.Error(), "agent endpoint") {
		t.Fatal("restricted endpoint policy changed", err)
	}
}
