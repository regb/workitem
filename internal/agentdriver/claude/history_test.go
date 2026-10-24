package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
