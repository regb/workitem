package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/testutil"
)

// Live text links to the transcript record that holds it, so readers can drop
// it once history shows that record.
func TestHeadlessAssistantFrameMarksItsRecord(t *testing.T) {
	socket := filepath.Join(testutil.ShortTempDir(t), "content.sock")
	server, err := protocol.ListenContent(socket, "runtime", "item", false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	s := headlessContent{server: server, emit: func(protocol.RuntimeEvent) {}}
	raw, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "Done."}})
	if err := s.frame(headlessFrame{Type: "stream_event", Event: raw}); err != nil {
		t.Fatal(err)
	}
	f := headlessFrame{Type: "assistant", UUID: "11111111-2222-3333-4444-555555555555"}
	f.Message.Content = json.RawMessage(`[{"type":"text","text":"Done."}]`)
	if err := s.frame(f); err != nil {
		t.Fatal(err)
	}
	content, err := protocol.ReadContent(context.Background(), socket, "runtime", "item")
	if err != nil || content.Text != "Done." || !reflect.DeepEqual(content.SavedAs, []string{f.UUID}) {
		t.Fatalf("content=%+v err=%v", content, err)
	}
}

func TestTranscriptFeedMarksTheRecordHoldingTheText(t *testing.T) {
	dir := testutil.ShortTempDir(t)
	path := filepath.Join(dir, "native.jsonl")
	record := `{"type":"assistant","uuid":"answer-record","timestamp":"2026-01-01T00:00:00Z","message":{"content":[{"type":"text","text":"Saved answer"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := protocol.ListenContent(filepath.Join(dir, "content.sock"), "runtime", "item", false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := (&transcriptFeed{}).refresh(path, server); err != nil {
		t.Fatal(err)
	}
	content, err := protocol.ReadContent(context.Background(), filepath.Join(dir, "content.sock"), "runtime", "item")
	if err != nil || content.Text != "Saved answer" || !reflect.DeepEqual(content.SavedAs, []string{"answer-record"}) {
		t.Fatalf("content=%+v err=%v", content, err)
	}
}

// Claude displays a session recap after a turn settles. It is not a new turn,
// while late batches of the reply being shown still belong to the settled one.
func TestFramesKeepWhatDecodesWhenAFieldChangesShape(t *testing.T) {
	f, dropped, err := decodeHeadlessFrame([]byte(`{"type":"system","subtype":"status","status":{"phase":"requesting"},"session_id":"s","uuid":"u"}`))
	if err != nil || f.Type != "system" || f.Subtype != "status" || f.SessionID != "s" || len(dropped) != 1 || dropped[0] != "status" {
		t.Fatalf("frame=%+v dropped=%v err=%v", f, dropped, err)
	}
	if _, _, err := decodeHeadlessFrame([]byte(`not json`)); err == nil {
		t.Fatal("a line that is not JSON was accepted")
	}
	if f, dropped, err := decodeHeadlessFrame([]byte(`{"type":"result","subtype":"success"}`)); err != nil || f.Type != "result" || dropped != nil {
		t.Fatalf("a well-formed frame changed: %+v %v %v", f, dropped, err)
	}
}
