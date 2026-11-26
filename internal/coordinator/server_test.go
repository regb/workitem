package coordinator

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"

	"github.com/regb/workitem/internal/testutil"
)

// The coordinator socket is private to its user and removed when the server
// closes.
func TestServerSocketIsPrivateAndRemovedOnClose(t *testing.T) {
	root, runtime := t.TempDir(), testutil.ShortTempDir(t)
	socket, err := SocketPath(runtime, root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(context.Background()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (&Client{SocketPath: socket}).Ping(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket info=%v err=%v", info, err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("server did not stop")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket remains after close: %v", err)
	}
}

func TestActivityBarrierIndexesPromptBeforeReturningSnapshot(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Truncate(time.Microsecond)
	manifest := testManifest(importTestID, "barrier", "Barrier", now)
	manifest.PrimaryConversation = &model.Conversation{Driver: "pi", ID: "session-1", Locator: "sessions/pi/session-1.jsonl"}
	seedItems(t, root, manifest)
	sessionPath := filepath.Join(store.New(root).ItemDir(manifest.ID), filepath.FromSlash(manifest.PrimaryConversation.NativeLocator()))
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	appendMessage := func(at time.Time, role, text string) {
		f, err := os.OpenFile(sessionPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		value := map[string]any{"type": "message", "timestamp": at.Format(time.RFC3339Nano), "message": map[string]any{"role": role, "content": []map[string]any{{"type": "text", "text": text}}}}
		if err := json.NewEncoder(f).Encode(value); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage(now, "assistant", "initial")
	socket, _ := SocketPath(testutil.ShortTempDir(t), root)
	server, err := NewServer(root, socket, testConversationDecoders)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	promptAt := now.Add(time.Second)
	appendMessage(promptAt, "user", "PRIVATE_BARRIER_PROMPT")
	result, err := server.activityBarrier(context.Background())
	if err != nil || !result.Projection.Fresh || result.ConversationImport.EntriesScanned != 1 || result.ConversationImport.BytesRead == 0 {
		t.Fatalf("barrier=%+v err=%v", result, err)
	}
	var observation AgentObservation
	found, err := server.database.ReadProjection(AgentObservationProjection, manifest.ID, &observation)
	if err != nil || !found || observation.Activity.LastRequestedAt == nil || !observation.Activity.LastRequestedAt.Equal(promptAt) {
		t.Fatalf("observation=%+v found=%v err=%v", observation, found, err)
	}
	databaseBytes, err := os.ReadFile(DatabasePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(databaseBytes), "PRIVATE_BARRIER_PROMPT") {
		t.Fatal("barrier copied prompt content into wi.db")
	}
}

func TestServerRejectsProtocolMismatch(t *testing.T) {
	root, runtime := t.TempDir(), testutil.ShortTempDir(t)
	socket, _ := SocketPath(runtime, root)
	server, err := NewServer(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go server.Serve(context.Background())
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"protocol_version":999,"id":"bad","method":"ping"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	n, err := conn.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buffer[:n]), "protocol_mismatch") {
		t.Fatalf("response = %s", buffer[:n])
	}
}

func TestServerRefusesToReplaceNonSocket(t *testing.T) {
	path := filepath.Join(testutil.ShortTempDir(t), "wi", "blocked.sock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(t.TempDir(), path); err == nil {
		t.Fatal("expected non-socket refusal")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "do not replace" {
		t.Fatalf("non-socket changed: %q err=%v", content, err)
	}
}
