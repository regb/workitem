package primaryagent

import (
	"encoding/json"
	"testing"
)

func TestSummarizePiSessionLineUsesCurrentMessageStopReason(t *testing.T) {
	tests := []struct {
		name, reason     string
		terminal, failed bool
	}{{"stop", "stop", true, false}, {"error", "error", true, true}, {"aborted", "aborted", true, true}, {"length", "length", true, true}, {"tool use", "toolUse", false, false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`{"type":"message","timestamp":"2026-08-10T22:04:43.463Z","message":{"role":"assistant","content":[{"type":"text","text":"answer"}],"stopReason":"` + tt.reason + `"}}`)
			got, err := summarizePiSessionLine(1, raw)
			if err != nil {
				t.Fatal(err)
			}
			if got.StopReason != tt.reason || got.Terminal != tt.terminal || got.Failed != tt.failed {
				t.Fatalf("summary=%+v", got)
			}
		})
	}
}
func TestSummarizePiSessionLineSupportsStringContent(t *testing.T) {
	raw := []byte(`{"type":"message","timestamp":"2026-08-10T22:04:28.897Z","message":{"role":"user","content":"[wi request ABC]\nhello"}}`)
	got, err := summarizePiSessionLine(7, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != "user" || got.TextPreview != "[wi request ABC]\nhello" {
		t.Fatalf("summary=%+v", got)
	}
	var line rawPiSessionLine
	if err := json.Unmarshal(raw, &line); err != nil {
		t.Fatal(err)
	}
	if text := piLineText(line); text != "[wi request ABC]\nhello" {
		t.Fatalf("text=%q", text)
	}
}
func TestSummarizePiSessionLineSupportsLegacyTopLevelStopReason(t *testing.T) {
	raw := []byte(`{"type":"message","timestamp":"2026-08-10T22:04:43.463Z","stopReason":"error","message":{"role":"assistant","content":[{"type":"text","text":"failed"}]}}`)
	got, err := summarizePiSessionLine(1, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.StopReason != "error" || !got.Failed {
		t.Fatalf("summary=%+v", got)
	}
}
