package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func historyFixture(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestHistoryToolsBranchesAndPartialAppend(t *testing.T) {
	path := historyFixture(t,
		`{"type":"queue-operation"}`,
		`{"type":"user","uuid":"u","parentUuid":null,"message":{"content":"task"}}`,
		`{"type":"attachment","uuid":"meta","parentUuid":"u"}`,
		`{"type":"assistant","uuid":"a","parentUuid":"meta","message":{"content":[{"type":"thinking","thinking":"reason"},{"type":"tool_use","id":"t","name":"Read","input":{"path":"a.txt"}}]}}`,
		`{"type":"user","uuid":"r","parentUuid":"a","message":{"content":[{"type":"tool_result","tool_use_id":"t","is_error":true,"content":[{"type":"text","text":"not found"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"eA=="}}]}]}}`,
		`{"type":"assistant","uuid":"done","parentUuid":"r","message":{"content":[{"type":"text","text":"finished"}]}}`,
		`{"type":"assistant","uuid":"sub","parentUuid":"u","isSidechain":true,"message":{"content":"subagent secret"}}`,
		`{"type":"assistant","uuid":"other","parentUuid":"u","message":{"content":"other branch"}}`)
	h, err := ReadHistoryPage(context.Background(), path, "done", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Messages) != 3 || h.Messages[1].Tools[0].Status != "failed" || h.Messages[1].Tools[0].Output != "not found" || len(h.Messages[1].Tools[0].Images) != 1 || len(h.NativeEntryIDs) != 4 {
		t.Fatalf("%+v", h)
	}
	if h.Messages[1].NativeIDs[0] != "r" || h.Messages[2].Text != "finished" {
		t.Fatalf("%+v", h.Messages)
	}
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = file.WriteString(`{"type":"assistant"`)
	_ = file.Close()
	latest, err := ReadHistoryPage(context.Background(), path, "", "")
	if err != nil || latest.LeafID != "other" || len(latest.Messages) != 2 {
		t.Fatalf("%+v %v", latest, err)
	}
	if _, err := ReadHistoryPage(context.Background(), path, "done", "other"); err == nil {
		t.Fatal("accepted cursor on another branch")
	}
}
func TestHistoryPagingAndBounds(t *testing.T) {
	lines := []string{}
	parent := ""
	for i := 0; i < 450; i++ {
		id := fmt.Sprintf("m%d", i)
		data, _ := json.Marshal(map[string]any{"type": "user", "uuid": id, "parentUuid": parent, "message": map[string]any{"content": id}})
		lines = append(lines, string(data))
		parent = id
	}
	path := historyFixture(t, lines...)
	seen := map[string]bool{}
	before := ""
	for {
		h, err := ReadHistoryPage(context.Background(), path, "m449", before)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range h.Messages {
			if seen[m.ID] {
				t.Fatal("duplicate paged entry")
			}
			seen[m.ID] = true
		}
		if !h.HasMore {
			break
		}
		if h.BeforeID == "" {
			t.Fatal("missing cursor")
		}
		before = h.BeforeID
	}
	if len(seen) != 450 {
		t.Fatal(len(seen))
	}
	data, _ := json.Marshal(map[string]any{"type": "assistant", "uuid": "large", "message": map[string]any{"content": strings.Repeat("界", 50000)}})
	h, err := ReadHistoryPage(context.Background(), historyFixture(t, string(data)), "", "")
	if err != nil || !h.Truncated || len(h.Messages[0].Text) > 64<<10 {
		t.Fatalf("%+v %v", h, err)
	}
}
func TestHistoryCompactionAndResultAcrossPageBoundary(t *testing.T) {
	lines := []string{`{"type":"assistant","uuid":"call","message":{"content":[{"type":"tool_use","id":"t","name":"Bash","input":{"command":"echo x"}}]}}`}
	parent := "call"
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("between-%d", i)
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","uuid":%q,"parentUuid":%q,"message":{"content":"progress"}}`, id, parent))
		parent = id
	}
	lines = append(lines, fmt.Sprintf(`{"type":"user","uuid":"result","parentUuid":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}}`, parent))
	path := historyFixture(t, lines...)
	h, err := ReadHistoryPage(context.Background(), path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	tool := h.Messages[len(h.Messages)-1].Tools[0]
	if tool.Name != "Bash" || tool.Input == "" || tool.Output != "x" || !h.HasMore {
		t.Fatalf("tool=%+v page=%+v", tool, h)
	}
	compact := historyFixture(t, `{"type":"system","subtype":"compact_boundary","uuid":"boundary","content":"Compacted earlier work"}`, `{"type":"user","isMeta":true,"isCompactSummary":true,"uuid":"summary","parentUuid":"boundary","message":{"content":"summary text"}}`)
	h, err = ReadHistoryPage(context.Background(), compact, "", "")
	if err != nil || len(h.Messages) != 2 || h.Messages[0].Role != "summary" || h.Messages[1].Role != "summary" || h.Messages[1].Text != "summary text" {
		t.Fatalf("%+v %v", h, err)
	}
}

func TestHistoryInvalidAndCanceled(t *testing.T) {
	for _, line := range []string{`bad`, `{"type":"user","uuid":"a","parentUuid":"a"}`, `{"type":"user","uuid":"a","parentUuid":"missing"}`} {
		if _, err := ReadHistoryPage(context.Background(), historyFixture(t, line), "", ""); err == nil {
			t.Fatal("accepted corrupt history")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadHistoryPage(ctx, historyFixture(t, `{"type":"queue-operation"}`), "", ""); err == nil {
		t.Fatal("ignored cancellation")
	}
}

// A background subagent: its call links to the transcript Claude keeps beside
// the session, and the harness's notification of its result becomes a report.
func TestHistorySubagentsAndTheirNotifications(t *testing.T) {
	path := historyFixture(t,
		`{"type":"user","uuid":"u","parentUuid":null,"message":{"content":"research"}}`,
		`{"type":"assistant","uuid":"a","parentUuid":"u","message":{"content":[{"type":"tool_use","id":"call","name":"Agent","input":{"description":"Look around","prompt":"look"}},{"type":"tool_use","id":"lost","name":"Agent","input":{}}]}}`,
		`{"type":"user","uuid":"r","parentUuid":"a","message":{"content":[{"type":"tool_result","tool_use_id":"call","content":"Async agent launched"}]}}`,
		`{"type":"user","uuid":"n","parentUuid":"r","origin":{"kind":"task-notification"},"message":{"content":"<task-notification>\n<task-id>abc1</task-id>\n<tool-use-id>call</tool-use-id>\n<status>completed</status>\n<summary>Agent \"Look around\" finished</summary>\n<result>[harness: neutralized]\n\nFound it. A </result> in the text stays.</result>\n</task-notification>"}}`,
		`{"type":"user","uuid":"fake","parentUuid":"n","message":{"content":"<task-notification><task-id>abc1</task-id></task-notification>"}}`)
	dir := subagentDir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"agent-abc1.meta.json": `{"agentType":"general-purpose","description":"Look around","toolUseId":"call","finished":true}`,
		"agent-abc1.jsonl":     `{"type":"user","uuid":"s1","parentUuid":null,"isSidechain":true,"message":{"content":"look"}}` + "\n" + `{"type":"assistant","uuid":"s2","parentUuid":"s1","isSidechain":true,"message":{"content":[{"type":"tool_use","id":"b","name":"Bash","input":{"command":"ls"}}]}}` + "\n",
		"agent-bad.meta.json":  `not json`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h, err := ReadHistoryPage(context.Background(), path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	tools := h.Messages[1].Tools
	if len(h.Messages) != 4 || tools[0].Subagent == nil || *tools[0].Subagent != (protocol.HistorySubagent{ID: "abc1", ToolID: "call", Type: "general-purpose", Description: "Look around", Status: "completed"}) || tools[1].Subagent != nil {
		t.Fatalf("calls %+v", h.Messages)
	}
	report := h.Messages[2]
	if report.Role != "subagent" || report.Subagent == nil || report.Subagent.ID != "abc1" || report.Subagent.ToolID != "call" || report.Subagent.Description != `Agent "Look around" finished` || report.Text != "Found it. A </result> in the text stays." {
		t.Fatalf("report %+v %+v", report, report.Subagent)
	}
	if typed := h.Messages[3]; typed.Role != "user" || typed.Subagent != nil {
		t.Fatalf("a typed message became a report: %+v", typed)
	}
	sub, err := ReadSubagentHistoryPage(context.Background(), path, "abc1", "")
	if err != nil || len(sub.Messages) != 2 || sub.Messages[0].Text != "look" || sub.Messages[1].Tools[0].Name != "Bash" {
		t.Fatalf("subagent %+v %v", sub, err)
	}
	for _, bad := range []string{"../abc1", "missing", ""} {
		if _, err := ReadSubagentHistoryPage(context.Background(), path, bad, ""); err == nil {
			t.Fatalf("read subagent %q", bad)
		}
	}
}

// Claude records parallel calls one after another; the first call's result
// hangs off that call, beside the branch, and still completes it.
func TestHistoryParallelCallResultsOffTheBranch(t *testing.T) {
	path := historyFixture(t,
		`{"type":"user","uuid":"u","parentUuid":null,"message":{"content":"go"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u","message":{"content":[{"type":"tool_use","id":"t1","name":"WebFetch","input":{"url":"https://a.test"}}]}}`,
		`{"type":"assistant","uuid":"a2","parentUuid":"a1","message":{"content":[{"type":"tool_use","id":"t2","name":"WebFetch","input":{"url":"https://b.test"}}]}}`,
		`{"type":"user","uuid":"r1","parentUuid":"a1","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"page a"}]}}`,
		`{"type":"user","uuid":"r2","parentUuid":"a2","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"page b"}]}}`,
		`{"type":"assistant","uuid":"done","parentUuid":"r2","message":{"content":"read both"}}`,
		`{"type":"user","uuid":"other","parentUuid":"a1","message":{"content":[{"type":"tool_result","tool_use_id":"t9","content":"unrelated"}]}}`)
	h, err := ReadHistoryPage(context.Background(), path, "done", "")
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{}
	for _, m := range h.Messages {
		for _, tool := range m.Tools {
			outputs[tool.ID] = tool.Status + ":" + tool.Output
		}
	}
	if outputs["t1"] != "completed:page a" || outputs["t2"] != "completed:page b" || len(outputs) != 2 {
		t.Fatalf("%v", outputs)
	}
}
