package claude

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestHeadlessAPIContentAndParallelToolPrivacy(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "content.sock")
	server, err := protocol.ListenContent(socket, "runtime", "item", false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	events := []protocol.RuntimeEvent{}
	s := headlessContent{server: server, emit: func(e protocol.RuntimeEvent) { events = append(events, e) }}
	stream := func(event any) {
		t.Helper()
		raw, _ := json.Marshal(event)
		if err := s.frame(headlessFrame{Type: "stream_event", Event: raw}); err != nil {
			t.Fatal(err)
		}
	}
	stream(map[string]any{"type": "message_start"})
	stream(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "first"}})
	f := headlessFrame{Type: "assistant"}
	f.Message.Content = json.RawMessage(`[{"type":"thinking","thinking":"private reasoning"}]`)
	if err := s.frame(f); err != nil {
		t.Fatal(err)
	}
	stream(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]string{"type": "text_delta", "text": " second"}})
	content, err := protocol.ReadContent(context.Background(), socket, "runtime", "item")
	if err != nil || content.Text != "first second" || content.Activity != "Assistant responding" {
		t.Fatal(content, err)
	}
	for i, name := range []string{"Bash", "Read"} {
		stream(map[string]any{"type": "content_block_start", "index": i + 2, "content_block": map[string]string{"type": "tool_use", "id": name, "name": name}})
		stream(map[string]any{"type": "content_block_delta", "index": i + 2, "delta": map[string]string{"type": "input_json_delta", "partial_json": "private tool input"}})
	}
	f.Type = "user"
	f.Message.Content = json.RawMessage(`[{"type":"tool_result","tool_use_id":"Bash","content":"private tool result","is_error":true}]`)
	if err := s.frame(f); err != nil {
		t.Fatal(err)
	}
	content, err = protocol.ReadContent(context.Background(), socket, "runtime", "item")
	if err != nil || content.Tool == nil || content.Tool.Name != "Bash" || content.Tool.Input != "private tool input" || content.Tool.Status != "failed" || content.Tool.Output != "private tool result" {
		t.Fatal(content, err)
	}
	for _, e := range events {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), "private") {
			t.Fatal("tool content entered compact events")
		}
	}
	stream(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]string{"type": "text_delta", "text": strings.Repeat("é", maxDisplayText)}})
	content, err = protocol.ReadContent(context.Background(), socket, "runtime", "item")
	if err != nil || !content.Truncated || len(content.Text) > maxDisplayText {
		t.Fatal(err)
	}
	for i := 0; i < 129; i++ {
		s.rememberTool(&protocol.ContentTool{ID: uuid(string(rune(i)))})
	}
	if len(s.tools) > 128 {
		t.Fatal("unbounded tool context")
	}
}
