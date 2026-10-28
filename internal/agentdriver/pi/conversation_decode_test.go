package pi

import "testing"

func TestSummarizeConversationFactUsesPiStopReason(t *testing.T) {
	tests := []struct {
		name, reason     string
		terminal, failed bool
	}{{"stop", "stop", true, false}, {"error", "error", true, true}, {"aborted", "aborted", true, false}, {"length", "length", true, true}, {"tool use", "toolUse", false, false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(`{"type":"message","timestamp":"2026-08-10T22:04:43.463Z","message":{"role":"assistant","content":[{"type":"text","text":"answer"}],"stopReason":"` + tt.reason + `"}}`)
			got, ok := SummarizeConversationFact(1, raw)
			if !ok {
				t.Fatal("line was not decoded")
			}
			if got.StopReason != tt.reason && !(tt.reason == "toolUse" && got.StopReason == "other") || got.Terminal != tt.terminal || got.Failed != tt.failed {
				t.Fatalf("summary=%+v", got)
			}
		})
	}
}

func TestSummarizeConversationFactSupportsStringContent(t *testing.T) {
	raw := []byte(`{"type":"message","timestamp":"2026-08-10T22:04:28.897Z","message":{"role":"user","content":"hello"}}`)
	got, ok := SummarizeConversationFact(7, raw)
	if !ok || got.Role != "user" || len(got.ContentTypes) != 1 || got.ContentTypes[0] != "text" {
		t.Fatalf("summary=%+v ok=%v", got, ok)
	}
}
