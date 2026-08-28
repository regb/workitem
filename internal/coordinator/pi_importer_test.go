package coordinator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func TestPiImporterIndexesIncrementallyWithoutConversationContent(t *testing.T) {
	root, itemID, sessionID := t.TempDir(), importTestID, "01KZYHHK91GWS6FZFQZQTT1KJ2"
	relative := filepath.Join("sessions", "pi", sessionID+".jsonl")
	path := filepath.Join(root, "items", itemID, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	values := []map[string]any{
		{"type": "message", "timestamp": at.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "PRIVATE_USER_PROMPT"}}}},
		{"type": "message", "timestamp": at.Add(time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "toolCall", "name": "bash", "arguments": map[string]any{"secret": "PRIVATE_TOOL_ARGUMENT"}}}}},
		{"type": "toolResult", "timestamp": at.Add(2 * time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "toolResult", "content": []map[string]any{{"type": "text", "text": "PRIVATE_TOOL_RESULT"}}}},
		{"type": "message", "timestamp": at.Add(3 * time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "assistant", "stopReason": "stop", "content": []map[string]any{{"type": "text", "text": "PRIVATE_ASSISTANT_RESPONSE"}}}},
	}
	var contents bytes.Buffer
	for _, value := range values {
		encoded, _ := json.Marshal(value)
		contents.Write(encoded)
		contents.WriteByte('\n')
	}
	if err := os.WriteFile(path, contents.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manifest := model.NewManifest(itemID, "pi-index", "Pi index", nil, false, at, model.Repository{}, model.Checkout{})
	manifest.RootPiSession = &model.PiSession{ID: sessionID, Path: relative}
	encodedManifest, _ := json.Marshal(manifest)
	digest := sha256.Sum256(encodedManifest)
	if _, err := db.SyncImportedManifests(map[string]ImportedManifest{itemID: {Manifest: manifest, Digest: hex.EncodeToString(digest[:])}}, true); err != nil {
		t.Fatal(err)
	}
	first, err := ImportPiSessions(context.Background(), db, root)
	if err != nil || first.FilesImported != 1 || first.EntriesScanned != 4 || first.MalformedLines != 0 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	var index PiSessionIndex
	if ok, err := db.ReadProjection(PiSessionProjection, itemID, &index); err != nil || !ok || index.InferredTurnState != "idle" || index.LastUserPrompt == nil || index.LastTerminalAssistant == nil || index.LastToolActivity == nil {
		t.Fatalf("index=%+v ok=%v err=%v", index, ok, err)
	}
	var activity AttentionActivity
	if ok, err := db.ReadProjection(AttentionActivityProjection, itemID, &activity); err != nil || !ok || activity.LastRequestedAt == nil || activity.LastCompletedAt == nil {
		t.Fatalf("activity=%+v ok=%v err=%v", activity, ok, err)
	}
	databaseBytes, err := os.ReadFile(DatabasePath(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PRIVATE_USER_PROMPT", "PRIVATE_TOOL_ARGUMENT", "PRIVATE_TOOL_RESULT", "PRIVATE_ASSISTANT_RESPONSE"} {
		if bytes.Contains(databaseBytes, []byte(secret)) {
			t.Fatalf("database contains Pi content %q", secret)
		}
	}
	second, err := ImportPiSessions(context.Background(), db, root)
	if err != nil || second.EntriesScanned != 0 || second.BytesRead != 0 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}
