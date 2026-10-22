package pi

import (
	"os"
	"path/filepath"
	"testing"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
)

func TestSessionEntryIDFindsTheNewestMatchingMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	lines := `{"type":"session","id":"s"}
{"type":"message","id":"user-1","message":{"role":"user","timestamp":100}}
{"type":"message","id":"answer-1","message":{"role":"assistant","timestamp":200}}
not json
{"type":"model_change","id":"m","timestamp":200}
{"type":"message","id":"result-1","message":{"role":"toolResult","timestamp":300}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	if id, found := sessionEntryID(path, "assistant", 200); !found || id != "answer-1" {
		t.Fatalf("id=%q found=%v", id, found)
	}
	if _, found := sessionEntryID(path, "assistant", 300); found {
		t.Fatal("matched a different role")
	}
	if _, found := sessionEntryID(filepath.Join(t.TempDir(), "missing"), "assistant", 200); found {
		t.Fatal("found an entry in a missing file")
	}
}

func TestThinkingUpdatesBecomeThoughtsOrActivity(t *testing.T) {
	update := func(kind, delta string) agent.RuntimeEvent {
		return agent.RuntimeEvent{Data: map[string]any{"assistant_message_event": map[string]any{"type": kind, "delta": delta}}}
	}
	if got := thinkingDelta(update("thinking_delta", "weighing")); got != "weighing" {
		t.Fatalf("thought = %q", got)
	}
	if got := thinkingDelta(update("text_delta", "answer")); got != "" {
		t.Fatalf("answer text taken as thought: %q", got)
	}
	if updateKind(update("thinking_start", "")) != "thinking_start" {
		t.Fatal("thinking start not recognized")
	}
}

// A submitted prompt is paired with the saved user message of the same text,
// and its session entry is reported once Pi has appended it.
func TestPromptsAreLinkedToTheirSessionEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := &driverprotocol.ContentServer{}
	transport := &nativeRPCTransport{spec: ExecSpec{SessionPath: path}, content: content}
	transport.prompts = []submittedPrompt{{requestID: "web:a", text: "same"}, {requestID: "web:b", text: "same"}, {requestID: "web:c", text: "other"}}
	userMessage := func(text string, timestamp float64) agent.RuntimeEvent {
		return agent.RuntimeEvent{Role: "user", Text: text, Data: map[string]any{"message": map[string]any{"role": "user", "timestamp": timestamp}}}
	}
	transport.matchPrompt(userMessage("  same ", 100))
	transport.matchPrompt(userMessage("unknown", 150))
	// Pi has not appended the entry yet: nothing is reported.
	transport.resolveSaved()
	if snapshot := readSnapshot(content); len(snapshot.Prompts) != 0 {
		t.Fatalf("reported before the entry exists: %+v", snapshot.Prompts)
	}
	if err := os.WriteFile(path, []byte(`{"type":"message","id":"entry-a","message":{"role":"user","timestamp":100}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport.resolveSaved()
	links := readSnapshot(content).Prompts
	if len(links) != 1 || links[0].RequestID != "web:a" || links[0].NativeIDs[0] != "entry-a" {
		t.Fatalf("links = %+v", links)
	}
	// The oldest prompt of that text was used; the next one still waits.
	if len(transport.prompts) != 2 || transport.prompts[0].requestID != "web:b" {
		t.Fatalf("prompts = %+v", transport.prompts)
	}
}

func readSnapshot(content *driverprotocol.ContentServer) driverprotocol.Content {
	return content.Snapshot()
}

// Pi starts and ends messages for prompts and tool results too; only the
// assistant's own messages are live text, so parallel calls stay listed.
func TestOnlyAssistantMessagesResetLiveContent(t *testing.T) {
	content := &driverprotocol.ContentServer{}
	transport := &nativeRPCTransport{content: content}
	transport.liveDisabled.Store(true)
	emit := func(event agent.RuntimeEvent) {
		if err := transport.emit(event); err != nil {
			t.Fatal(err)
		}
	}
	emit(agent.RuntimeEvent{Type: "message.started", Role: "assistant"})
	emit(agent.RuntimeEvent{Type: agent.EventMessageDelta, Text: "Running both"})
	emit(agent.RuntimeEvent{Type: agent.EventToolStarted, ToolCallID: "a", ToolName: "bash"})
	emit(agent.RuntimeEvent{Type: agent.EventToolStarted, ToolCallID: "b", ToolName: "bash"})
	emit(agent.RuntimeEvent{Type: agent.EventToolCompleted, ToolCallID: "a", ToolName: "bash"})
	emit(agent.RuntimeEvent{Type: "message.started", Role: "toolResult"})
	emit(agent.RuntimeEvent{Type: agent.EventMessageCompleted, Role: "toolResult"})
	value := content.Snapshot()
	if value.Text != "Running both" || len(value.Tools) != 2 || value.Activity == "Message saved" {
		t.Fatalf("tool result reset live content: %+v", value)
	}
}

func TestRefusedPromptsAreForgotten(t *testing.T) {
	transport := &nativeRPCTransport{prompts: []submittedPrompt{{requestID: "web:a", text: "go"}, {requestID: "web:b", text: "go"}}}
	transport.forgetPrompt("web:a")
	if len(transport.prompts) != 1 || transport.prompts[0].requestID != "web:b" {
		t.Fatalf("prompts = %+v", transport.prompts)
	}
}
