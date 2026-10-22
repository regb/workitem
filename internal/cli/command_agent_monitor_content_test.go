package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestContentPrinterStreamsTextAndListsChanges(t *testing.T) {
	var out bytes.Buffer
	p := &contentPrinter{w: &out}
	for _, event := range []driverprotocol.ContentEvent{
		// The previous turn's saved text is not printed again.
		{Type: driverprotocol.ContentSnapshot, Snapshot: &driverprotocol.Content{RuntimeID: "r1", Activity: "Idle", Text: "done", SavedAs: []string{"old"}}},
		{Type: driverprotocol.ContentMessageStarted},
		{Type: driverprotocol.ContentThoughtDelta, Text: "weighing"},
		{Type: driverprotocol.ContentThoughtDelta, Text: " options"},
		{Type: driverprotocol.ContentMessageDelta, Text: "Hel"},
		{Type: driverprotocol.ContentMessageDelta, Text: "lo"},
		{Type: driverprotocol.ContentToolStarted, Tool: &driverprotocol.ContentTool{Name: "bash", Status: "running", Input: "{\n  \"command\": \"ls -la\\necho\"\n}"}},
		{Type: driverprotocol.ContentToolUpdated, Tool: &driverprotocol.ContentTool{Name: "bash", Status: "running", Output: "a"}},
		{Type: driverprotocol.ContentToolCompleted, Tool: &driverprotocol.ContentTool{Name: "bash", Status: "completed"}},
		{Type: driverprotocol.ContentMessageCompleted},
		{Type: driverprotocol.ContentMessageSaved, NativeIDs: []string{"entry-1"}},
		{Type: driverprotocol.ContentTurnSettled},
	} {
		if err := p.print(event); err != nil {
			t.Fatal(err)
		}
	}
	want := strings.Join([]string{
		"runtime: r1  activity: Idle",
		"thinking: weighing options",
		"Hello",
		"tool running: bash  ls -la",
		"tool completed: bash",
		"saved as entry-1",
		"turn settled",
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestContentPrinterWritesOneJSONEventPerLine(t *testing.T) {
	var out bytes.Buffer
	p := &contentPrinter{w: &out, json: true}
	_ = p.print(driverprotocol.ContentEvent{Sequence: 4, Type: driverprotocol.ContentMessageDelta, Text: "a\nb"})
	_ = p.print(driverprotocol.ContentEvent{Sequence: 5, Type: driverprotocol.ContentTurnSettled})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var first driverprotocol.ContentEvent
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &first) != nil || first.Text != "a\nb" {
		t.Fatalf("lines = %q", lines)
	}
}
