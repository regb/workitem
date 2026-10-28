package claude

import (
	"os"
	"testing"
	"time"
)

func TestObserveIgnoresPartialWritesAndSummaries(t *testing.T) {
	path := historyFixture(t,
		`{"type":"user","timestamp":"2026-10-12T10:00:00Z","message":{"content":"task"}}`,
		`{"type":"user","isCompactSummary":true,"timestamp":"2026-10-12T10:01:00Z","message":{"content":"summary"}}`,
		`{"type":"assistant","timestamp":"2026-10-12T10:02:00Z","message":{"stop_reason":"end_turn","content":"answer"}}`)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"user","message":{"content":"[Request interrupted by user]"}}`)
	_ = f.Close()
	observation, warnings := observe(path, time.Now())
	if len(warnings) != 0 || observation.InferredTurnState != "idle" || observation.LastUserPrompt.Timestamp.Minute() != 0 {
		t.Fatalf("%+v %v", observation, warnings)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString("\n")
	_ = f.Close()
	observation, warnings = observe(path, time.Now())
	// The interrupt ends the turn the operator stopped; it is not a failure.
	if len(warnings) != 0 || observation.InferredTurnState != "idle" || observation.LastTurnActivity.StopReason != "interrupted" || observation.LastUserPrompt.Timestamp.Minute() != 0 {
		t.Fatalf("%+v %v", observation, warnings)
	}
}
