package driverprotocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// contentFixture records events and the snapshot after each, so other
// implementations of ApplyContent (the browser's) can check they agree.
type contentFixture struct {
	Events    []ContentEvent `json:"events"`
	Snapshots []Content      `json:"snapshots"`
}

const contentFixturePath = "testdata/content_events.json"

func TestContentFixtureMatchesApplyContent(t *testing.T) {
	events := []ContentEvent{
		{Sequence: 1, Type: ContentSnapshot, Snapshot: &Content{ProtocolVersion: ContentVersion, RuntimeID: "runtime", WorkItemID: "item", Sequence: 1, Activity: "Idle", LeafID: "leaf-1"}},
		{Sequence: 2, Type: ContentMessageStarted, MessageID: "live-2", Activity: "Assistant responding"},
		{Sequence: 3, Type: ContentMessageDelta, MessageID: "live-2", Text: "Hel", Activity: "Assistant responding"},
		{Sequence: 4, Type: ContentMessageDelta, MessageID: "live-2", Text: "lo 🌍"},
		{Sequence: 5, Type: ContentToolStarted, Tool: &ContentTool{ID: "t1", Name: "bash", Status: "running", Input: `{"command":"ls"}`}, Activity: "Tool running: bash"},
		{Sequence: 6, Type: ContentThoughtDelta, Text: "Checking the files"},
		{Sequence: 7, Type: ContentToolStarted, Tool: &ContentTool{ID: "t0", Name: "read", Status: "running"}},
		{Sequence: 8, Type: ContentToolUpdated, Tool: &ContentTool{ID: "t1", Name: "bash", Status: "running", Input: `{"command":"ls"}`, Output: "a"}},
		{Sequence: 9, Type: ContentToolCompleted, Tool: &ContentTool{ID: "t1", Name: "bash", Status: "failed", Input: `{"command":"ls"}`, Output: "a\nb"}, Activity: "Tool failed: bash"},
		{Sequence: 10, Type: ContentMessageReplaced, Text: "Rewritten", Truncated: true},
		{Sequence: 11, Type: ContentToolCleared},
		{Sequence: 12, Type: ContentActivity, Activity: "", LeafID: "leaf-2"},
		{Sequence: 13, Type: ContentMessageCompleted, Activity: "Message saved"},
		{Sequence: 14, Type: ContentMessageSaved, NativeIDs: []string{"entry-1", "entry-2"}},
		{Sequence: 15, Type: ContentPromptSaved, RequestID: "web-1", NativeIDs: []string{"user-1"}},
		{Sequence: 16, Type: ContentPromptSaved, RequestID: "web-2", NativeIDs: []string{"user-2"}},
		{Sequence: 17, Type: ContentPromptSaved, RequestID: "web-1", NativeIDs: []string{"user-1b"}},
		{Sequence: 18, Type: ContentTurnSettled, Activity: "Idle"},
		{Sequence: 19, Type: ContentMessageStarted, MessageID: "live-19"},
		{Sequence: 20, Type: ContentMessageDelta, Text: "partial"},
		{Sequence: 21, Type: ContentMessageSaved, NativeIDs: []string{"entry-3"}},
		{Sequence: 22, Type: ContentMessageDelta, Text: " more"},
	}
	fixture := contentFixture{Events: events}
	var value Content
	for _, event := range events {
		value = ApplyContent(value, event)
		fixture.Snapshots = append(fixture.Snapshots, value)
	}
	if os.Getenv("WI_UPDATE_FIXTURES") != "" {
		encoded, _ := json.MarshalIndent(fixture, "", "  ")
		if err := os.WriteFile(contentFixturePath, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := os.ReadFile(filepath.FromSlash(contentFixturePath))
	if err != nil {
		t.Fatalf("%v (set WI_UPDATE_FIXTURES=1 to write it)", err)
	}
	var want contentFixture
	if err := json.Unmarshal(saved, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, fixture) {
		t.Fatal("content fixture is stale; run with WI_UPDATE_FIXTURES=1 and review the change")
	}
}

// Truncation stays out of the fixture to keep it small; each implementation
// tests it on its own.
func TestApplyContentTruncatesAtACharacterBoundary(t *testing.T) {
	value := ApplyContent(Content{}, ContentEvent{Sequence: 1, Type: ContentMessageDelta, Text: strings.Repeat("x", MaxContentTextBytes-2) + "🌍 tail"})
	if !value.Truncated || len(value.Text) != MaxContentTextBytes-2 {
		t.Fatalf("truncated=%v bytes=%d", value.Truncated, len(value.Text))
	}
	if value = ApplyContent(value, ContentEvent{Sequence: 2, Type: ContentMessageDelta, Text: "more"}); len(value.Text) != MaxContentTextBytes-2 {
		t.Fatal("text grew after truncation")
	}
}
