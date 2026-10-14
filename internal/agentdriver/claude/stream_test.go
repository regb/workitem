package claude

import (
	"context"
	"encoding/json"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDisplayStreamOrderingAndTranscriptLag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.sock")
	server, err := protocol.ListenContent(path, "runtime", "item", false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	turns := &turnTracker{promptID: "prompt", started: time.Now().Add(-time.Second)}
	s := &displayStream{server: server, turns: turns}
	snapshot := func() protocol.Content {
		c, e := protocol.ReadContent(context.Background(), path, "runtime", "item")
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	chunk := func(message string, index int, final bool, delta string) {
		ok, e := s.accept(displayInput{hookInput: hookInput{PromptID: "prompt", ObservedAt: time.Now()}, displayFields: displayFields{TurnID: "turn", MessageID: message, Index: index, Final: final, Delta: delta}})
		if e != nil || !ok {
			t.Fatalf("chunk refused: %v", e)
		}
	}
	s.PublishTranscript("old-leaf", "old text", "Message saved", nil)
	chunk("message", 0, false, "First line\n")
	if snapshot().Text != "First line\n" {
		t.Fatal(snapshot())
	}
	s.PublishTranscript("user-leaf", "old text", "Working", nil)
	if snapshot().Text != "First line\n" {
		t.Fatal("lag overwrote stream")
	}
	if ok, _ := s.accept(displayInput{hookInput: hookInput{PromptID: "prompt"}, displayFields: displayFields{TurnID: "turn", MessageID: "message", Index: 0, Delta: "duplicate"}}); ok {
		t.Fatal("duplicate appended")
	}
	chunk("message", 1, true, "")
	if !s.final {
		t.Fatal("empty final lost")
	}
	s.PublishTranscript("native-assistant", "First line\n", "Message saved", nil)
	if s.active || snapshot().LeafID != "native-assistant" {
		t.Fatal(snapshot())
	}
	chunk("second", 0, false, "Next ")
	chunk("second", 2, true, "gap")
	if !snapshot().Truncated || !strings.Contains(snapshot().Activity, "missing") {
		t.Fatal(snapshot())
	}
	s.PublishPromptTranscript("old", "old text", "Message saved", nil, "older-prompt", time.Now())
	if !s.active {
		t.Fatal("old native prompt replaced missing stream")
	}
	s.PublishPromptTranscript("new-leaf", "Next complete native text", "Message saved", nil, "prompt", time.Now())
	if s.active || snapshot().Truncated || snapshot().Text != "Next complete native text" {
		t.Fatal(snapshot())
	}
	if ok, _ := s.accept(displayInput{hookInput: hookInput{PromptID: "prompt"}, displayFields: displayFields{TurnID: "turn", MessageID: "message", Index: 2, Final: true, Delta: "old"}}); ok {
		t.Fatal("retired message replaced new one")
	}
	if ok, _ := s.accept(displayInput{hookInput: hookInput{PromptID: "old"}, displayFields: displayFields{TurnID: "turn", MessageID: "late", Index: 0, Delta: "late"}}); ok {
		t.Fatal("late prompt display accepted")
	}
}
func TestDisplayHookDoesNotUseSemanticEndpoint(t *testing.T) {
	hookPath := filepath.Join(t.TempDir(), "hook.sock")
	server, err := protocol.ListenControl(displaySocketPath(hookPath))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunHook(context.Background(), hookPath, strings.NewReader(`{"hook_event_name":"MessageDisplay","session_id":"session","prompt_id":"prompt","turn_id":"turn","message_id":"message","index":0,"final":false,"delta":"private text"}`))
	}()
	select {
	case request := <-server.Requests():
		var h displayInput
		if err := json.Unmarshal([]byte(request.Command.Message), &h); err != nil {
			t.Fatal(err)
		}
		if h.Delta != "private text" || h.PromptID != "prompt" || h.ObservedAt.IsZero() || request.Command.Type != "display" {
			t.Fatal(h)
		}
		request.Respond(nil)
	case <-time.After(time.Second):
		t.Fatal("no private batch")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestDisplayBoundsUTF8(t *testing.T) {
	v, truncated := boundedDisplay(strings.Repeat("é", maxDisplayDelta), maxDisplayDelta-1)
	if !truncated || len(v) > maxDisplayDelta-1 || strings.HasSuffix(v, "\xc3") {
		t.Fatal("invalid bound")
	}
}
