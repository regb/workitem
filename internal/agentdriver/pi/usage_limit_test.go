package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

// usageLimitReply is the empty reply Pi records when the provider refuses a
// turn for a usage limit.
const usageLimitReply = `{"type":"message","id":"limit","parentId":"root","timestamp":"2026-11-15T09:00:00.000Z","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"Codex error: The usage limit has been reached","provider":"openai-codex","model":"gpt-5.5"}}`

func TestAUsageLimitEndsTheTurnWithItsReason(t *testing.T) {
	fact, valid := SummarizeConversationFact(3, []byte(usageLimitReply))
	if !valid || !fact.Terminal || !fact.Failed || fact.StopReason != "usage_limit" || fact.UsageProvider != "codex" {
		t.Fatalf("fact=%+v valid=%v", fact, valid)
	}
	if driverprotocol.RuntimeReasonText(fact.StopReason) == "" {
		t.Fatal("usage_limit has no reason text")
	}
	// Other provider errors keep failing without the usage limit reason.
	other, _ := SummarizeConversationFact(4, []byte(`{"type":"message","id":"x","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"Codex error: internal server error"}}`))
	if !other.Failed || !other.Terminal || other.StopReason != "error" || other.UsageProvider != "" {
		t.Fatalf("server error fact=%+v", other)
	}
}

func TestHistoryShowsAProviderErrorAsANotice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.jsonl")
	data := `{"type":"session","version":3,"id":"session"}
{"type":"message","id":"root","parentId":null,"message":{"role":"user","content":"hello"}}
` + usageLimitReply + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadHistory(context.Background(), path, "")
	if err != nil || len(result.Messages) != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	if notice := result.Messages[1]; !notice.Error || notice.Text() != "Codex error: The usage limit has been reached" || notice.UsageProvider != "codex" || result.Messages[0].Error {
		t.Fatalf("messages=%+v", result.Messages)
	}
}

func TestALiveUsageLimitSettlesWithItsReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &nativeRPCTransport{spec: ExecSpec{RuntimeID: "owner", WorkItemID: "item"}, liveContext: ctx, liveNonce: "nonce", liveQueue: make(chan queuedLiveEvent, 8), work: map[string]bool{}}
	delivered := make(chan driverprotocol.RuntimeEvent, 8)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case queued := <-transport.liveQueue:
				delivered <- queued.event
				if queued.ack != nil {
					close(queued.ack)
				}
			}
		}
	}()
	settle := func(provider, errorMessage string) driverprotocol.RuntimeEvent {
		t.Helper()
		end, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{}, "stopReason": "error", "errorMessage": errorMessage, "provider": provider}})
		for _, raw := range [][]byte{end, []byte(`{"type":"agent_settled"}`)} {
			if err := transport.handleOutput(raw); err != nil {
				t.Fatal(err)
			}
		}
		for event := range delivered {
			if event.Type == driverprotocol.EventAgentSettled {
				return event
			}
		}
		return driverprotocol.RuntimeEvent{}
	}
	if event := settle("openai-codex", "Codex error: The usage limit has been reached"); !event.Failed || event.ReasonCode != "usage_limit" || event.UsageProvider != "codex" {
		t.Fatalf("usage limit settlement: %+v", event)
	}
	if event := settle("openai-codex", "Codex error: internal server error"); !event.Failed || event.ReasonCode != "" || event.UsageProvider != "" {
		t.Fatalf("other failure settlement: %+v", event)
	}
}

// A usage limit belongs to the provider of the model that hit it, named as
// the usage plugin names it.
func TestAUsageLimitNamesTheModelsProvider(t *testing.T) {
	for provider, want := range map[string]string{"anthropic": "claude", "openai-codex": "codex", "opencode-go": "opencode-go", "": ""} {
		line, _ := json.Marshal(map[string]any{"type": "message", "id": "limit", "message": map[string]any{"role": "assistant", "content": []any{}, "stopReason": "error", "errorMessage": "429 rate limit exceeded", "provider": provider}})
		if fact, _ := SummarizeConversationFact(1, line); fact.StopReason != "usage_limit" || fact.UsageProvider != want {
			t.Errorf("provider %q: fact=%+v, want %q", provider, fact, want)
		}
	}
}
