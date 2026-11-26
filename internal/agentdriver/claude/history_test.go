package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	if len(h.Messages) != 3 || h.Messages[1].Tools()[0].Status != "failed" || h.Messages[1].Tools()[0].Output != "not found" || len(h.Messages[1].Tools()[0].Images) != 1 || len(h.NativeEntryIDs) != 4 {
		t.Fatalf("%+v", h)
	}
	if h.Messages[1].NativeIDs[0] != "r" || h.Messages[2].Text() != "finished" {
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
	if err != nil || !h.Truncated || len(h.Messages[0].Text()) > 64<<10 {
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
	tool := h.Messages[len(h.Messages)-1].Tools()[0]
	if tool.Name != "Bash" || tool.Input == "" || tool.Output != "x" || !h.HasMore {
		t.Fatalf("tool=%+v page=%+v", tool, h)
	}
	compact := historyFixture(t, `{"type":"system","subtype":"compact_boundary","uuid":"boundary","content":"Conversation compacted","compactMetadata":{"trigger":"auto","preTokens":288117}}`, `{"type":"user","isMeta":true,"isCompactSummary":true,"uuid":"summary","parentUuid":"boundary","message":{"content":"summary text"}}`)
	h, err = ReadHistoryPage(context.Background(), compact, "", "")
	if err != nil || len(h.Messages) != 2 || h.Messages[0].Role != "summary" || h.Messages[1].Role != "summary" || h.Messages[1].Text() != "summary text" {
		t.Fatalf("%+v %v", h, err)
	}
	if boundary := h.Messages[0]; boundary.Text() != "" || *boundary.Summary != (protocol.HistorySummary{Kind: "compaction", TokensBefore: 288117}) || *h.Messages[1].Summary != (protocol.HistorySummary{Kind: "compaction"}) {
		t.Fatalf("compaction %+v %+v", h.Messages[0], h.Messages[1].Summary)
	}
}

// A compaction boundary starts a new parent chain; the conversation before it
// stays readable through the boundary's logical parent.
func TestHistoryContinuesBeforeACompaction(t *testing.T) {
	path := historyFixture(t,
		`{"type":"user","uuid":"old","parentUuid":null,"message":{"content":"before"}}`,
		`{"type":"assistant","uuid":"reply","parentUuid":"old","message":{"id":"m","content":[{"type":"text","text":"earlier answer"}]}}`,
		`{"type":"system","subtype":"compact_boundary","uuid":"boundary","parentUuid":null,"logicalParentUuid":"reply","compactMetadata":{"preTokens":1}}`,
		`{"type":"user","isMeta":true,"isCompactSummary":true,"uuid":"summary","parentUuid":"boundary","message":{"content":"summary text"}}`,
		`{"type":"user","uuid":"new","parentUuid":"summary","message":{"content":"after"}}`,
	)
	h, err := ReadHistoryPage(context.Background(), path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{}
	for _, message := range h.Messages {
		texts = append(texts, message.Text())
	}
	if got := strings.Join(texts, "|"); got != "before|earlier answer||summary text|after" {
		t.Fatalf("history %q", got)
	}
	// Paging from the boundary reaches the earlier conversation too.
	if h, err = ReadHistoryPage(context.Background(), path, "", "boundary"); err != nil || len(h.Messages) != 2 || h.Messages[1].Text() != "earlier answer" {
		t.Fatalf("%+v %v", h, err)
	}
	// Claude can name a logical parent it wrote after the boundary, while
	// compacting, which descends from the boundary: the record written before
	// the boundary continues the history instead.
	late := historyFixture(t,
		`{"type":"user","uuid":"old","parentUuid":null,"message":{"content":"before"}}`,
		`{"type":"system","subtype":"compact_boundary","uuid":"boundary","parentUuid":null,"logicalParentUuid":"reminder"}`,
		`{"type":"attachment","uuid":"reminder","parentUuid":"boundary","attachment":{"type":"total_tokens_reminder"}}`,
		`{"type":"user","uuid":"new","parentUuid":"reminder","message":{"content":"after"}}`,
	)
	if h, err = ReadHistoryPage(context.Background(), late, "", ""); err != nil || len(h.Messages) != 3 || h.Messages[0].Text() != "before" {
		t.Fatalf("%+v %v", h, err)
	}
	// A logical parent no longer in the transcript ends the history there.
	trimmed := historyFixture(t, `{"type":"system","subtype":"compact_boundary","uuid":"boundary","parentUuid":null,"logicalParentUuid":"gone"}`)
	if h, err = ReadHistoryPage(context.Background(), trimmed, "", ""); err != nil || len(h.Messages) != 1 {
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
	tools := h.Messages[1].Tools()
	if len(h.Messages) != 4 || tools[0].Subagent == nil || *tools[0].Subagent != (protocol.HistorySubagent{ID: "abc1", ToolID: "call", Type: "general-purpose", Description: "Look around", Status: "completed"}) || tools[1].Subagent != nil {
		t.Fatalf("calls %+v", h.Messages)
	}
	report := h.Messages[2]
	if report.Role != "subagent" || report.Subagent == nil || report.Subagent.ID != "abc1" || report.Subagent.ToolID != "call" || report.Subagent.Description != `Agent "Look around" finished` || report.Text() != "Found it. A </result> in the text stays." {
		t.Fatalf("report %+v %+v", report, report.Subagent)
	}
	if typed := h.Messages[3]; typed.Role != "user" || typed.Subagent != nil {
		t.Fatalf("a typed message became a report: %+v", typed)
	}
	sub, err := ReadSubagentHistoryPage(context.Background(), path, "abc1", "")
	if err != nil || len(sub.Messages) != 2 || sub.Messages[0].Text() != "look" || sub.Messages[1].Tools()[0].Name != "Bash" {
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
		for _, tool := range m.Tools() {
			outputs[tool.ID] = tool.Status + ":" + tool.Output
		}
	}
	if outputs["t1"] != "completed:page a" || outputs["t2"] != "completed:page b" || len(outputs) != 2 {
		t.Fatalf("%v", outputs)
	}
}

// Claude records an interrupt as a user line; history shows it as the mark
// of a reply the operator stopped, not as their message.
func TestHistoryShowsAnInterruptAsAnAbortedReply(t *testing.T) {
	path := historyFixture(t,
		`{"type":"user","uuid":"u","parentUuid":null,"message":{"content":"task"}}`,
		`{"type":"assistant","uuid":"a","parentUuid":"u","message":{"content":[{"type":"text","text":"partial"}]}}`,
		`{"type":"user","uuid":"stop","parentUuid":"a","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`)
	h, err := ReadHistoryPage(context.Background(), path, "stop", "")
	if err != nil || len(h.Messages) != 3 {
		t.Fatalf("%+v %v", h, err)
	}
	if mark := h.Messages[2]; mark.Role != "assistant" || !mark.Aborted || mark.Text() != "" {
		t.Fatalf("interrupt: %+v", mark)
	}
}

func TestHistoryKeepsThatClaudeReasonedAndForHowLong(t *testing.T) {
	path := historyFixture(t,
		`{"type":"user","uuid":"ask","timestamp":"2026-10-14T15:11:30.000Z","message":{"content":"why?"}}`,
		`{"type":"assistant","uuid":"intro","parentUuid":"ask","timestamp":"2026-10-14T15:11:30.500Z","message":{"id":"m","content":[{"type":"text","text":"Let me see."}]}}`,
		`{"type":"assistant","uuid":"think","parentUuid":"intro","timestamp":"2026-10-14T15:11:31.776Z","message":{"id":"m","content":[{"type":"thinking","thinking":"","signature":"sig"}]}}`,
		`{"type":"assistant","uuid":"answer","parentUuid":"think","timestamp":"2026-10-14T15:11:33.195Z","message":{"id":"m","content":[{"type":"text","text":"because"}]}}`)
	h, err := ReadHistoryPage(context.Background(), path, "", "")
	if err != nil || len(h.Messages) != 2 {
		t.Fatalf("%+v %v", h, err)
	}
	// The reply's records are one message; its reasoning is where Claude
	// wrote it, measured from the question, not the earlier block.
	reply := h.Messages[1]
	parts := reply.Parts
	if len(parts) != 3 || parts[0].Text != "Let me see." || parts[1].Thought == nil || *parts[1].Thought != (protocol.Thought{DurationMS: 1776}) || parts[2].Text != "because" {
		t.Fatalf("reply %+v", parts)
	}
	if reply.ID != "intro" || !reflect.DeepEqual(reply.NativeIDs, []string{"think", "answer"}) {
		t.Fatalf("reply ids %q %v", reply.ID, reply.NativeIDs)
	}
}

// Claude saves each block of a reply as its own record, with the results of
// its calls among them; history shows the reply as one message, its blocks
// in the order Claude wrote them.
func TestHistoryKeepsAnInterleavedReplyInOrder(t *testing.T) {
	path := historyFixture(t,
		`{"type":"user","uuid":"ask","timestamp":"2026-10-14T15:11:30.000Z","message":{"content":"go"}}`,
		`{"type":"assistant","uuid":"intro","parentUuid":"ask","timestamp":"2026-10-14T15:11:30.500Z","message":{"id":"m","content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","uuid":"call","parentUuid":"intro","timestamp":"2026-10-14T15:11:31.000Z","message":{"id":"m","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","uuid":"result","parentUuid":"call","timestamp":"2026-10-14T15:11:32.000Z","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a.go"}]}}`,
		`{"type":"assistant","uuid":"then","parentUuid":"result","timestamp":"2026-10-14T15:11:33.000Z","message":{"id":"m","content":[{"type":"text","text":"then"}]}}`,
		`{"type":"assistant","uuid":"think","parentUuid":"then","timestamp":"2026-10-14T15:11:34.000Z","message":{"id":"m","content":[{"type":"thinking","thinking":"","signature":"sig"}]}}`,
		`{"type":"assistant","uuid":"next","parentUuid":"think","timestamp":"2026-10-14T15:11:35.000Z","message":{"id":"n","content":[{"type":"text","text":"another reply"}]}}`)
	h, err := ReadHistoryPage(context.Background(), path, "", "")
	if err != nil || len(h.Messages) != 3 {
		t.Fatalf("%+v %v", h, err)
	}
	reply := h.Messages[1]
	parts := reply.Parts
	if len(parts) != 4 || parts[0].Text != "first" || parts[1].Tool == nil || parts[2].Text != "then" || parts[3].Thought == nil {
		t.Fatalf("parts %+v", parts)
	}
	if tool := parts[1].Tool; tool.ID != "toolu_1" || tool.Status != "completed" || tool.Output != "a.go" {
		t.Fatalf("tool %+v", tool)
	}
	if !reflect.DeepEqual(reply.NativeIDs, []string{"call", "result", "then", "think"}) {
		t.Fatalf("native ids %v", reply.NativeIDs)
	}
	if h.Messages[2].Text() != "another reply" {
		t.Fatalf("next reply %+v", h.Messages[2])
	}
}

// A message sent into a running turn is recorded as a queued_command
// attachment; it shows as the operator's message where it was delivered.
func TestHistoryShowsSteeredMessages(t *testing.T) {
	path := historyFixture(t,
		`{"type":"user","uuid":"u","parentUuid":null,"message":{"content":"task"}}`,
		`{"type":"assistant","uuid":"a","parentUuid":"u","message":{"content":[{"type":"text","text":"working"}]}}`,
		`{"type":"attachment","uuid":"s","parentUuid":"a","attachment":{"type":"queued_command","commandMode":"prompt","source_uuid":"sent-by-wi","prompt":[{"type":"text","text":"also check this"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"eA=="}}]}}`,
		`{"type":"attachment","uuid":"n","parentUuid":"s","attachment":{"type":"queued_command","commandMode":"task-notification","prompt":"<task-notification>done</task-notification>"}}`,
		`{"type":"attachment","uuid":"p","parentUuid":"n","attachment":{"type":"queued_command","prompt":"plain text"}}`,
		`{"type":"assistant","uuid":"done","parentUuid":"p","message":{"content":[{"type":"text","text":"checked"}]}}`)
	h, err := ReadHistoryPage(context.Background(), path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, m := range h.Messages {
		got = append(got, m.Role+":"+m.Text())
	}
	if strings.Join(got, "|") != "user:task|assistant:working|user:also check this|user:plain text|assistant:checked" || len(h.Messages[2].Images()) != 1 {
		t.Fatalf("%v %+v", got, h.Messages[2])
	}
	// Claude saves a steered message under a uuid of its own; it answers to
	// the one wi sent it with too, which the send was linked to.
	if ids := h.Messages[2].NativeIDs; len(ids) != 1 || ids[0] != "sent-by-wi" {
		t.Fatalf("steered message ids: %v", ids)
	}
}

func TestHistoryShowsPastedTextAsSent(t *testing.T) {
	path := historyFixture(t,
		`{"type":"user","uuid":"u","parentUuid":null,"message":{"content":"\n\n<pasted_content id=\"b3aa\">\n<wi-message-1 from=\"item:A\">\nhello\n</wi-message-1>\n</pasted_content id=\"b3aa\">\n"}}`,
		`{"type":"user","uuid":"v","parentUuid":"u","message":{"content":[{"type":"text","text":"look: <pasted_content id=\"c1\">\nline\n</pasted_content> and <pasted_content id=\"x\">kept</pasted_content id=\"y\">"}]}}`,
		`{"type":"assistant","uuid":"a","parentUuid":"v","message":{"content":"<pasted_content id=\"z\">\nmine\n</pasted_content id=\"z\">"}}`)
	h, err := ReadHistoryPage(context.Background(), path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Messages[0].Text(); got != "<wi-message-1 from=\"item:A\">\nhello\n</wi-message-1>" {
		t.Fatalf("pasted message = %q", got)
	}
	if got := h.Messages[1].Text(); got != "look: line and <pasted_content id=\"x\">kept</pasted_content id=\"y\">" {
		t.Fatalf("mixed message = %q", got)
	}
	if got := h.Messages[2].Text(); got != "<pasted_content id=\"z\">\nmine\n</pasted_content id=\"z\">" {
		t.Fatalf("assistant text changed: %q", got)
	}
}
