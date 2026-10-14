package claude

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestSettledTranscriptRecoversMissingDisplayBeforeHookStart(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "content.sock")
	server, err := protocol.ListenContent(socket, "runtime", "item", false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
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
	value, err := protocol.ReadContent(context.Background(), socket, "runtime", "item")
	if err != nil || stream.active || value.Text != "Complete native answer" || value.Truncated || value.LeafID != "current-leaf" {
		t.Fatal(value, err)
	}
}
