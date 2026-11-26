package claude

import (
	"context"
	"encoding/json"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/testutil"
)

func TestDisplayStreamOrderingAndTranscriptLag(t *testing.T) {
	server, snapshot := listenContent(t, filepath.Join(testutil.ShortTempDir(t), "content.sock"))
	turns := &turnTracker{promptID: "prompt", started: time.Now().Add(-time.Second)}
	s := &displayStream{server: server, turns: turns}
	chunk := func(message string, index int, final bool, delta string) {
		ok, e := s.accept(displayInput{hookInput: hookInput{PromptID: "prompt", ObservedAt: time.Now()}, displayFields: displayFields{TurnID: "turn", MessageID: message, Index: index, Final: final, Delta: delta}})
		if e != nil || !ok {
			t.Fatalf("chunk refused: %v", e)
		}
	}
	s.PublishTranscript("old-leaf", "old text", "Message saved", nil)
	chunk("message", 0, false, "First line\n")
	if snapshot().Text() != "First line\n" {
		t.Fatal(snapshot())
	}
	s.PublishTranscript("user-leaf", "old text", "Working", nil)
	if snapshot().Text() != "First line\n" {
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
	if s.active || snapshot().Truncated || snapshot().Text() != "Next complete native text" {
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
	hookPath := filepath.Join(testutil.ShortTempDir(t), "hook.sock")
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
func TestSettledTranscriptRecoversMissingDisplayBeforeHookStart(t *testing.T) {
	server, read := listenContent(t, filepath.Join(testutil.ShortTempDir(t), "content.sock"))
	now := time.Now()
	turns := &turnTracker{promptID: "current", started: now.Add(-time.Second)}
	stream := displayStream{server: server, turns: turns}
	// The first optional batch was lost. The observed final batch starts after
	// Claude committed the answer, so comparing against firstAt cannot recover.
	accepted, err := stream.accept(displayInput{hookInput: hookInput{PromptID: "current", ObservedAt: now}, displayFields: displayFields{TurnID: "turn", MessageID: "display", Index: 1, Final: true, Delta: "missing prefix"}})
	if err != nil || !accepted || !stream.incomplete {
		t.Fatal(accepted, err)
	}
	recorded := now.Add(-100 * time.Millisecond)
	stream.PublishPromptTranscript("current-leaf", "Complete native answer", "Message saved", nil, "current", recorded)
	if !stream.active {
		t.Fatal("in-flight display reconciled before native settlement")
	}
	turns.settled = true
	for _, stale := range []struct {
		prompt string
		at     time.Time
	}{{"previous", recorded}, {"current", turns.started.Add(-time.Second)}} {
		stream.PublishPromptTranscript("stale-leaf", "Stale answer", "Message saved", nil, stale.prompt, stale.at)
		if !stream.active {
			t.Fatal("settlement accepted stale transcript")
		}
	}
	stream.PublishPromptTranscript("current-leaf", "Complete native answer", "Message saved", nil, "current", recorded)
	if value := read(); stream.active || value.Text() != "Complete native answer" || value.Truncated || value.LeafID != "current-leaf" {
		t.Fatal(value)
	}
}
