package claude

import (
	"context"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

// usageLimitReply is the synthetic reply Claude Code writes when the API
// refuses a turn for a usage limit.
const usageLimitReply = `{"type":"assistant","uuid":"limit","parentUuid":"u","isApiErrorMessage":true,"error":"rate_limit","timestamp":"2026-10-22T09:23:50.199Z","message":{"role":"assistant","model":"<synthetic>","stop_reason":"stop_sequence","content":[{"type":"text","text":"You've hit your usage limit · resets 2am"}]}}`

func TestAUsageLimitEndsTheTurnWithItsReason(t *testing.T) {
	fact, valid := summarize(7, []byte(usageLimitReply))
	if !valid || !fact.Terminal || !fact.Failed || fact.StopReason != "usage_limit" {
		t.Fatalf("fact=%+v valid=%v", fact, valid)
	}
	if reason := settledReason(protocol.EventAgentSettled, true, settleReason(fact)); reason != "usage_limit" {
		t.Fatalf("settlement reason %q", reason)
	}
	if protocol.RuntimeReasonText("usage_limit") == "" {
		t.Fatal("usage_limit has no reason text")
	}
	// Other API errors keep failing without the usage limit reason.
	other, _ := summarize(8, []byte(`{"type":"assistant","uuid":"x","isApiErrorMessage":true,"error":"server_error","message":{"role":"assistant","content":[]}}`))
	if !other.Failed || other.StopReason == "usage_limit" {
		t.Fatalf("server error fact=%+v", other)
	}
}

func TestHistoryMarksHarnessReplies(t *testing.T) {
	path := historyFixture(t, `{"type":"user","uuid":"u","parentUuid":null,"message":{"content":"task"}}`, usageLimitReply)
	page, err := ReadHistoryPage(context.Background(), path, "", "")
	if err != nil || len(page.Messages) != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if page.Messages[0].Error || !page.Messages[1].Error || page.Messages[1].Text == "" {
		t.Fatalf("messages=%+v", page.Messages)
	}
}
