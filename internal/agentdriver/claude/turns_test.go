package claude

import (
	"encoding/json"
	protocol "github.com/regb/workitem/agentdriver/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeTurnIdentityFencesLateHooks(t *testing.T) {
	tracker := &turnTracker{}
	now := time.Now()
	if !tracker.accept(hookInput{Event: "UserPromptSubmit", PromptID: "a", ObservedAt: now}) || !tracker.accept(hookInput{Event: "UserPromptSubmit", PromptID: "b", ObservedAt: now.Add(time.Second)}) {
		t.Fatal("new prompt refused")
	}
	for _, event := range []string{"Stop", "StopFailure", "PreToolUse", "PostToolUse", "UserPromptSubmit"} {
		if tracker.accept(hookInput{Event: event, PromptID: "a", ObservedAt: now.Add(2 * time.Second)}) {
			t.Fatalf("late %s affected new prompt", event)
		}
	}
	if tracker.accept(hookInput{Event: "Stop", ObservedAt: now}) {
		t.Fatal("legacy old hook accepted")
	}
	if !tracker.accept(hookInput{Event: "Stop", PromptID: "b", ObservedAt: now.Add(2 * time.Second)}) {
		t.Fatal("current stop refused")
	}
	tracker.settled = true
	if tracker.accept(hookInput{Event: "Stop", PromptID: "b"}) {
		t.Fatal("duplicate settlement accepted")
	}
	if !tracker.matches("b", now.Add(3*time.Second)) {
		t.Fatal("same-prompt continuation cannot reactivate")
	}
	if tracker.matches("a", now.Add(3*time.Second)) {
		t.Fatal("old display accepted")
	}
}
func TestFreshTranscriptFailureIsNotDiscardedAsHistory(t *testing.T) {
	epoch := time.Now()
	dir := t.TempDir()
	server, err := protocol.ListenContent(filepath.Join(dir, "content.sock"), "runtime", "item", false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	path := filepath.Join(dir, "native.jsonl")
	var rows []byte
	for _, at := range []time.Time{epoch.Add(-time.Hour), epoch.Add(time.Second)} {
		row, _ := json.Marshal(map[string]any{"type": "system", "subtype": "informational", "preventContinuation": true, "content": "UserPromptSubmit operation blocked by hook:\nsafe test", "timestamp": at})
		rows = append(rows, append(row, '\n')...)
	}
	if err := os.WriteFile(path, rows, 0600); err != nil {
		t.Fatal(err)
	}
	feed := transcriptFeed{factAfter: epoch}
	facts, err := feed.refresh(path, server)
	if err != nil || len(facts) != 1 || facts[0].StopReason != "prompt_blocked" {
		t.Fatalf("facts %v: %v", facts, err)
	}
}
func TestBlockedPromptFactIsClosedAndContentFree(t *testing.T) {
	record := map[string]any{"type": "system", "subtype": "informational", "preventContinuation": true, "content": "UserPromptSubmit operation blocked by hook:\nsecret reason\nOriginal prompt: secret prompt", "timestamp": time.Now()}
	raw, _ := json.Marshal(record)
	fact, ok := summarize(1, raw)
	if !ok || !fact.Failed || !fact.Terminal || fact.StopReason != "prompt_blocked" {
		t.Fatal(fact)
	}
	encoded, _ := json.Marshal(fact)
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("native text leaked")
	}
	for _, field := range []string{"preventContinuation", "subtype", "isSidechain"} {
		copy := map[string]any{}
		for k, v := range record {
			copy[k] = v
		}
		switch field {
		case "preventContinuation":
			copy[field] = false
		case "subtype":
			copy[field] = "other"
		case "isSidechain":
			copy[field] = true
		}
		raw, _ = json.Marshal(copy)
		fact, _ = summarize(1, raw)
		if fact.Terminal {
			t.Fatal(field)
		}
	}
}
