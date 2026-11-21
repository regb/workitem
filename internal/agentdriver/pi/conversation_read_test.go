package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestOptionalConversationCapabilitiesDoNotLaunchBackend(t *testing.T) {
	response, err := handleConversationOperation(context.Background(), driverprotocol.Request{Operation: driverprotocol.OperationConversationCapabilities, Config: json.RawMessage(`{"executable":"/must-not-execute"}`), Conversation: &driverprotocol.ConversationRequest{}})
	if err != nil || response.Capabilities == nil || !response.Capabilities.History {
		t.Fatalf("capabilities %+v %v", response, err)
	}
}

func TestHistoryBoundsPreserveUnicodeAndDoNotModifyNativeContent(t *testing.T) {
	text := strings.Repeat("x", (16<<10)-1) + "🌍"
	record, err := json.Marshal(map[string]any{"type": "message", "id": "entry", "parentId": nil, "message": map[string]any{"role": "assistant", "content": text}})
	if err != nil {
		t.Fatal(err)
	}
	data := append(record, '\n')
	path := filepath.Join(t.TempDir(), "native.jsonl")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadHistory(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Messages) != 1 || !utf8.ValidString(result.Messages[0].Text) || strings.ContainsRune(result.Messages[0].Text, utf8.RuneError) {
		t.Fatalf("invalid bounded history %+v", result)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatal("read modified native content", err)
	}
	empty, err := ReadHistory(context.Background(), path, "-")
	if err != nil || len(empty.Messages) != 0 {
		t.Fatalf("empty live branch %+v %v", empty, err)
	}
}
