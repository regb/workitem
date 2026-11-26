package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/testutil"
)

func TestLiveTranscriptPartialToolFailureAndReplacement(t *testing.T) {
	dir := testutil.ShortTempDir(t)
	path := filepath.Join(dir, "native.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	server, read := listenContent(t, filepath.Join(dir, "content.sock"))
	feed := &transcriptFeed{}
	if _, err := feed.refresh(path, server); err != nil {
		t.Fatal(err)
	}
	appendData := func(data string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(data)
		_ = f.Close()
		if _, err := feed.refresh(path, server); err != nil {
			t.Fatal(err)
		}
	}
	appendData(`{"type":"assistant","uuid":"a","message":{"content":[{"type":"text","text":"private response"}]}}`)
	if got := read(); got.Sequence != 0 {
		t.Fatal("published incomplete line")
	}
	appendData("\n")
	first := read()
	if first.Text() != "private response" || first.LeafID != "a" || first.IdleGuard {
		t.Fatalf("%+v", first)
	}
	appendData(`{"type":"assistant","uuid":"call","message":{"content":[{"type":"tool_use","id":"tool","name":"Read","input":{"path":"secret-path"}}]}}` + "\n")
	appendData(`{"type":"user","uuid":"result","message":{"content":[{"type":"tool_result","tool_use_id":"tool","content":"private output"}]}}` + "\n")
	got := read()
	if tool := got.LastTool(); tool == nil || tool.Name != "Read" || tool.Status != "completed" || tool.Output != "private output" {
		t.Fatalf("%+v", got)
	}
	appendData(`{"type":"assistant","uuid":"sub","isSidechain":true,"message":{"content":"subagent text"}}` + "\n")
	if read().LeafID == "sub" {
		t.Fatal("sidechain changed main snapshot")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString(`{"type":"user","uuid":"interrupted","message":{"content":"[Request interrupted by user]"}}` + "\n")
	_ = f.Close()
	facts, err := feed.refresh(path, server)
	if err != nil || len(facts) != 1 || facts[0].Failed || !facts[0].Terminal {
		t.Fatalf("%+v %v", facts, err)
	}
	if !strings.Contains(read().Activity, "interrupted") {
		t.Fatal("lost interruption status")
	}
	if err := os.WriteFile(path, []byte(`{"type":"assistant","uuid":"rewind","message":{"content":"rewound"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := feed.refresh(path, server); err != nil {
		t.Fatal(err)
	}
	if read().Text() != "rewound" {
		t.Fatal("truncation retained stale content")
	}
	if _, err := protocol.ReadContent(context.Background(), filepath.Join(dir, "content.sock"), "old-runtime", "item"); err == nil {
		t.Fatal("accepted stale content owner")
	}
}
func TestFailureHookAndCompactInterruptionFacts(t *testing.T) {
	kind, failed := hookEvent("StopFailure")
	if kind != protocol.EventAgentSettled || !failed {
		t.Fatal(kind, failed)
	}
	if fact, valid := summarize(1, []byte(`{"type":"assistant","isApiErrorMessage":true,"message":{"content":"private API error"}}`)); !valid || !fact.Terminal || !fact.Failed {
		t.Fatalf("%+v", fact)
	}
	// An interrupt ends the turn without failing it.
	if fact, valid := summarize(1, []byte(`{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`)); !valid || !fact.Terminal || fact.Failed || fact.StopReason != "interrupted" {
		t.Fatalf("%+v", fact)
	}
}

// A prompt wi typed into the terminal is linked to its transcript record,
// found by Claude's prompt ID, as headless Claude links its prompts.
func TestTypedPromptsAreLinkedToTheirRecord(t *testing.T) {
	dir := testutil.ShortTempDir(t)
	path := filepath.Join(dir, "native.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	links := map[string][]string{}
	feed := &transcriptFeed{linked: func(request string, ids []string) { links[request] = ids }}
	feed.awaitPrompt("prompt-1", "page-1")
	records := `{"type":"user","uuid":"typed-elsewhere","promptId":"prompt-0","message":{"role":"user","content":"from the terminal"}}
{"type":"user","uuid":"user-1","promptId":"prompt-1","message":{"role":"user","content":"from the page"}}
{"type":"assistant","uuid":"reply-1","promptId":"prompt-1","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}
`
	if err := os.WriteFile(path, []byte(records), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := feed.refresh(path, ignoredTranscript{}); err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || strings.Join(links["page-1"], ",") != "user-1" || len(feed.awaiting) != 0 {
		t.Fatalf("links %v awaiting %v", links, feed.awaiting)
	}
}
