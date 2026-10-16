package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	pidriver "github.com/regb/workitem/internal/agentdriver/pi"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

func TestCoordinatorWaitAndQueueReturnsAtomicPostTransitionSnapshot(t *testing.T) {
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	// Agent turns complete before the test blocks an item, so blocking marks
	// every response read.
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ids := []string{"01KZYHGDCECSFS4BJ2SNTQP49V", "01KZYHGDCECSFS4BJ2SNTQP49W", "01KZYHGDCECSFS4BJ2SNTQP49X"}
	for index, id := range ids {
		manifest := model.NewManifest(id, "atomic-"+string(rune('a'+index)), "Atomic", nil, false, now.Add(time.Duration(index)*time.Second), model.Repository{}, model.Checkout{})
		manifest.State = model.StateActive
		sessionID := "session-" + string(rune('a'+index))
		manifest.PrimaryConversation = &model.Conversation{Driver: "pi", ID: sessionID, Locator: filepath.Join("sessions", "pi", sessionID+".jsonl")}
		if err := native.CreateItem(context.Background(), manifest); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(native.ItemDir(id), manifest.PrimaryConversation.NativeLocator())
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		requestedAt := now.Add(time.Duration(index) * time.Minute)
		for _, value := range []map[string]any{
			{"type": "message", "timestamp": requestedAt.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "private prompt"}}}},
			{"type": "message", "timestamp": requestedAt.Add(time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "assistant", "stopReason": "stop", "content": []map[string]any{{"type": "text", "text": "private answer"}}}},
		} {
			if err := json.NewEncoder(file).Encode(value); err != nil {
				file.Close()
				t.Fatal(err)
			}
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	seedDB, err := coordinator.OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	manifests, _ := native.ListManifests()
	values := map[string]coordinator.ImportedManifest{}
	for _, m := range manifests {
		encoded, _ := json.Marshal(m)
		digest := sha256.Sum256(encoded)
		values[m.ID] = coordinator.ImportedManifest{Manifest: m, Digest: hex.EncodeToString(digest[:])}
	}
	if _, err := seedDB.SyncImportedManifests(values, true); err != nil {
		seedDB.Close()
		t.Fatal(err)
	}
	if err := seedDB.Close(); err != nil {
		t.Fatal(err)
	}
	socket, err := coordinator.SocketPath(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := coordinator.NewServer(root, socket, coordinator.ConversationDecoders{"pi": func(_ context.Context, _ model.Conversation, records []coordinator.ConversationRecord) ([]coordinator.ConversationEventFact, []bool, error) {
		facts, decoded := make([]coordinator.ConversationEventFact, 0, len(records)), make([]bool, len(records))
		for i, record := range records {
			fact, ok := pidriver.SummarizeConversationFact(record.Line, record.Raw)
			decoded[i] = ok
			if ok {
				facts = append(facts, coordinator.ConversationEventFact{Line: fact.Line, Type: fact.Type, Role: fact.Role, Timestamp: fact.Timestamp, StopReason: fact.StopReason, ContentTypes: fact.ContentTypes, Terminal: fact.Terminal, Failed: fact.Failed})
			}
		}
		return facts, decoded, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	defer func() {
		server.Close()
		<-serveDone
	}()
	client := &coordinator.Client{SocketPath: socket}
	application := app.New(native, nil)
	application.Store = newCoordinatorStore(native, client)
	cfg := Config{App: application, Coordinator: client, Env: map[string]string{"WI_ID": ids[0]}}
	snapshot, snapshotSelection, err := coordinatorActionabilitySnapshot(context.Background(), cfg, nil)
	if err != nil || len(snapshot.Candidates) != 3 || snapshot.Candidates[0].Item.ID != ids[2] || snapshotSelection == nil || snapshotSelection.WorkItemID != ids[2] || !snapshotSelection.CurrentInQueue || !snapshotSelection.Wrapped {
		t.Fatalf("snapshot=%+v selection=%+v err=%v", snapshot, snapshotSelection, err)
	}
	waited, queue, waitSelection, err := coordinatorWaitAndQueue(context.Background(), cfg, ids[0], []model.Blocker{{Kind: model.BlockerExternal, Event: "waiting"}}, nil)
	if err != nil || !waited.Waiting || len(waited.Added) != 1 || len(queue.Candidates) != 2 || queue.Candidates[0].Item.ID != ids[2] || queue.Candidates[1].Item.ID != ids[1] || waitSelection == nil || waitSelection.WorkItemID != ids[2] {
		t.Fatalf("waited=%+v queue=%+v selection=%+v err=%v", waited, queue, waitSelection, err)
	}
	canonical, err := client.CanonicalManifest(context.Background(), ids[0])
	if err != nil || canonical.Manifest.State != model.StateActive || len(canonical.Manifest.Blockers) != 1 || canonical.Manifest.Blockers[0].Event != "waiting" {
		t.Fatalf("canonical=%+v err=%v", canonical, err)
	}
	assertAppBlockerCommands(t, application, client, ids)
}
