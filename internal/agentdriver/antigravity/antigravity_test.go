package antigravity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agentdriver/acp"
)

func TestVersionGates(t *testing.T) {
	for _, c := range []struct {
		version string
		want    bool
	}{{"1.5.0", true}, {"1.5.12", true}, {"2.0.0", true}, {"1.4.12", false}, {"agy_acp_server_1.1.1", false}, {"", false}, {"dev", false}} {
		if got := versionAtLeast(c.version, steeringVersion); got != c.want {
			t.Errorf("versionAtLeast(%q) = %v", c.version, got)
		}
	}
}

func TestReasoningMapsOntoTheModelsLevels(t *testing.T) {
	choices := []acp.ConfigChoice{{Value: "low"}, {Value: "high"}}
	for level, want := range map[string]string{"high": "high", "medium": "low", "max": "high", "off": "low", "low": "low"} {
		if got := reasoningValue(choices, level); got != want {
			t.Errorf("reasoningValue(%q) = %q, want %q", level, got, want)
		}
	}
	grouped := acp.Values([]acp.ConfigChoice{{GroupID: "g", Options: []acp.ConfigChoice{{Value: "a"}, {Value: "b"}}}, {Value: "c"}})
	if len(grouped) != 3 || grouped[2].Value != "c" {
		t.Fatalf("grouped choices: %v", grouped)
	}
}

func TestModelListIsParsed(t *testing.T) {
	// As agy 1.5.14 lists them: an ID and a name per line, a tab apart.
	listing := "Fetching available models...\ngemini-4-neon-high\tGemini 4 Neon (High)\ngemini-4-neon-xhigh\tGemini 4 Neon (XHigh)\ngemini-4-neon-low\tGemini 4 Neon (Low)\ngemini-pro-orchestrator-neon\tOrchestrator (Neon)\nclaude-opus-5-5-high\tClaude Opus 5.5 (High)\nclaude-opus-5-5-low\tClaude Opus 5.5 (Low)\ngpt-oss-120b-medium\tGPT-OSS 120B (Medium)\n"
	models := parseModels(listing, protocol.ModelCatalog{}).Models
	want := []protocol.ModelOption{
		{Value: "gemini-4-neon", Label: "Gemini 4 Neon", Reasoning: true, Levels: []string{"low", "high", "xhigh"}},
		{Value: "gemini-pro-orchestrator-neon", Label: "Orchestrator (Neon)"},
		{Value: "claude-opus-5-5", Label: "Claude Opus 5.5", Reasoning: true, Levels: []string{"low", "high"}},
		{Value: "gpt-oss-120b-medium", Label: "GPT-OSS 120B (Medium)"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models:\n%+v\nwant\n%+v", models, want)
	}
}

func TestToolsThatOutliveTheirPrompt(t *testing.T) {
	for _, c := range []struct {
		kind, title, input string
		want               bool
	}{
		{"execute", "Running run_command", `{"CommandLine":"make"}`, true},
		{"execute", "Running run_command", `{"CommandLine":"server","IsDaemon":true}`, false},
		{"other", "Run schedule", `{}`, true},
		{"read", "Running view_file", `{}`, false},
	} {
		if got := outlivesPrompt(c.kind, c.title, json.RawMessage(c.input)); got != c.want {
			t.Errorf("%s %s: %v", c.kind, c.title, got)
		}
	}
}

func TestJournalHistoryObservationAndFork(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions", "conversation.jsonl")
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	records := []Record{
		{Type: recordSession, ID: "s1", SessionID: "native-1", CWD: "/work"},
		{Type: recordUser, ID: "u1", Text: "first", Timestamp: now},
		{Type: recordAssistant, ID: "a1", Text: "reply", Thought: "hmm", Tools: []protocol.HistoryTool{{ID: "t", Name: "tool", Status: "completed"}}, StopReason: acp.StopEndTurn, Timestamp: now},
		{Type: recordUser, ID: "u2", Text: "second", Timestamp: now},
	}
	for _, r := range records {
		if err := j.append(r); err != nil {
			t.Fatal(err)
		}
	}
	j.close()
	// A write in progress does not count.
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	file.WriteString(`{"type":"assistant","id":"half`)
	file.Close()

	observed, _ := observe(path, now)
	if observed.InferredTurnState != "incomplete" || observed.LastUserPrompt == nil || observed.LastTerminalAssistant == nil || observed.MalformedLines != 0 {
		t.Fatalf("observation: %+v", observed)
	}
	page, err := historyPage(path, "", 2)
	if err != nil || len(page.Messages) != 2 || !page.HasMore || page.BeforeID != "a1" || page.LeafID != "u2" {
		t.Fatalf("page: %+v %v", page, err)
	}
	// An older journal's reply keeps its reasoning before its text.
	if parts := page.Messages[0].Parts; len(parts) != 3 || parts[0].Thought == nil || parts[0].Thought.Text != "hmm" || parts[1].Text != "reply" || parts[2].Tool == nil {
		t.Fatalf("reply: %+v", page.Messages[0])
	}
	older, err := historyPage(path, page.BeforeID, 2)
	if err != nil || len(older.Messages) != 1 || older.Messages[0].ID != "u1" || older.HasMore {
		t.Fatalf("older: %+v %v", older, err)
	}
	if session, cwd, _ := latestSession(path); session != "native-1" || cwd != "/work" {
		t.Fatalf("session %q %q", session, cwd)
	}

	target := filepath.Join(dir, "fork.jsonl")
	if err := forkJournal(path, target, "/elsewhere"); err != nil {
		t.Fatal(err)
	}
	if session, cwd, _ := latestSession(target); session != "native-1" || cwd != "/elsewhere" {
		t.Fatalf("fork session %q %q", session, cwd)
	}
	if err := forkJournal(path, target, "/elsewhere"); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("fork over an existing journal: %v", err)
	}
}

func TestQuotaAndFailuresAreFacts(t *testing.T) {
	quota := Record{Type: recordAssistant, ID: "a1", StopReason: acp.StopRefusal, Failed: true, Reason: "usage_limit"}
	f, ok := fact(3, quota)
	if !ok || !f.Failed || !f.Terminal || f.StopReason != "usage_limit" || f.UsageProvider != "antigravity" {
		t.Fatalf("quota fact: %+v", f)
	}
	// History shows the refusal as the notice it is, naming whose quota ran out.
	if m := assistantMessage(quota); !m.Error || m.Text() == "" || m.UsageProvider != "antigravity" {
		t.Fatalf("quota message: %+v", m)
	}
	if m := assistantMessage(Record{Type: recordAssistant, ID: "a2", Failed: true, Error: "boom"}); !m.Error || m.Text() != "boom" || m.UsageProvider != "" {
		t.Fatalf("failure message: %+v", m)
	}
	if _, ok := fact(1, Record{Type: recordSession}); ok {
		t.Fatal("a session record is no message")
	}
}

func TestInvokeIsAOneShotPrintRequest(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	agy := filepath.Join(dir, "agy")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + args + "\nenv | grep -c '^WI_' >> " + args + "\necho '  The answer  '\n"
	if err := os.WriteFile(agy, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WI_ITEM_ID", "secret-item")
	answer, err := Invoke(context.Background(), Config{Executable: agy, Model: "gemini-4-neon", Reasoning: "minimal"}, protocol.ModelInvokeRequest{Instructions: "Be brief.", Prompt: "-what does the readme say"})
	if err != nil || answer != "The answer" {
		t.Fatalf("answer %q %v", answer, err)
	}
	got, _ := os.ReadFile(args)
	if !strings.Contains(string(got), "--model\ngemini-4-neon\n") || strings.Contains(string(got), "--effort") || !strings.Contains(string(got), "--print=Be brief.\n\n-what does the readme say\n") || !strings.HasSuffix(string(got), "\n0\n") {
		t.Fatalf("agy arguments:\n%s", got)
	}
	if _, err := Invoke(context.Background(), Config{Executable: agy, Reasoning: "high"}, protocol.ModelInvokeRequest{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(args); !strings.Contains(string(got), "--effort\nhigh\n") {
		t.Fatalf("agy arguments:\n%s", got)
	}

	failing := filepath.Join(dir, "failing")
	os.WriteFile(failing, []byte("#!/bin/sh\necho 'error: not signed in' >&2\nexit 1\n"), 0700)
	if _, err := Invoke(context.Background(), Config{Executable: failing}, protocol.ModelInvokeRequest{Prompt: "task"}); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("failure: %v", err)
	}
}

func TestRepliesKeepTheirPartsInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	events := []protocol.RuntimeEvent{}
	rt := &runtime{journal: j, content: &protocol.ContentServer{}, emitFn: func(e protocol.RuntimeEvent) { events = append(events, e) }}
	rt.request.Runtime.ID = "r"
	rt.begin("c", "")
	chunk := func(kind, text string) acp.SessionUpdate {
		content, _ := json.Marshal(map[string]string{"type": "text", "text": text})
		return acp.SessionUpdate{Kind: kind, Content: content}
	}
	name, completed := "read_file", acp.ToolCompleted
	rt.update(chunk(acp.UpdateAgentMessage, "Let me read it."))
	rt.update(acp.SessionUpdate{Kind: acp.UpdateToolCall, ToolCallID: "t1", Name: &name})
	rt.update(chunk(acp.UpdateAgentMessage, "It says hello."))
	rt.update(chunk(acp.UpdateAgentThought, "done?"))
	rt.update(acp.SessionUpdate{Kind: acp.UpdateToolCallPatch, ToolCallID: "t1", Status: &completed})
	// Live content shows the reply as it is written.
	if parts := rt.content.Snapshot().Parts; len(parts) != 4 || parts[1].Tool == nil || parts[1].Tool.Status != "completed" || parts[2].Text != "It says hello." || parts[3].Thought == nil {
		t.Fatalf("live parts: %+v", parts)
	}
	rt.turn.stop = acp.StopCancelled
	rt.settle()
	page, err := historyPage(path, "", 10)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("page: %+v %v", page, err)
	}
	reply := page.Messages[0]
	parts := reply.Parts
	if len(parts) != 4 || parts[0].Text != "Let me read it." || parts[1].Tool == nil || parts[1].Tool.ID != "t1" || parts[1].Tool.Status != "completed" || parts[2].Text != "It says hello." || parts[3].Thought == nil || parts[3].Thought.Text != "done?" {
		t.Fatalf("parts: %+v", parts)
	}
	if !reply.Aborted || reply.ID != "a-r-1" {
		t.Fatalf("reply: %+v", reply)
	}
	// An older journal's steps read as parts in their order.
	older := assistantMessage(Record{Type: recordAssistant, ID: "a1", Steps: []Step{{Text: "Let me read it.", Tools: []protocol.HistoryTool{{ID: "t1", Name: "read_file", Status: "completed"}}}, {Text: "It says hello."}}})
	if parts := older.Parts; len(parts) != 3 || parts[0].Text != "Let me read it." || parts[1].Tool == nil || parts[2].Text != "It says hello." {
		t.Fatalf("older parts: %+v", parts)
	}
}

func TestAUsageLimitSettlesNamingAntigravity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	var settled protocol.RuntimeEvent
	rt := &runtime{journal: j, content: &protocol.ContentServer{}, emitFn: func(e protocol.RuntimeEvent) {
		if e.Type == protocol.EventAgentSettled {
			settled = e
		}
	}}
	rt.begin("c", "")
	rt.turn.failed, rt.turn.reason = true, "usage_limit"
	rt.settle()
	if !settled.Failed || settled.ReasonCode != "usage_limit" || settled.UsageProvider != "antigravity" {
		t.Fatalf("settled: %+v", settled)
	}
}

// ACP has no skills, so agy gets wi's in its appended system prompt, with
// the operator's instructions and where to put what the operator should
// see: its own artifacts folder is invisible to them.
func TestSystemPromptCarriesSkillsAndWhereDeliverablesGo(t *testing.T) {
	rt := &runtime{request: protocol.LaunchRequest{
		AppendSystemPrompt: "Be brief.",
		Skills:             []protocol.Skill{{Name: "wi", Files: []protocol.SkillFile{{Path: "SKILL.md", Data: []byte("Attach reports with wi attach.")}, {Path: "notes.txt", Data: []byte("ignored")}}}},
	}}
	rt.init.AgentCapabilities.Meta = json.RawMessage(`{"antigravity":{"systemPrompt":{"append":true}}}`)
	var meta struct {
		Antigravity struct {
			SystemPrompt struct {
				Append string `json:"append"`
			} `json:"systemPrompt"`
		} `json:"antigravity"`
	}
	if err := json.Unmarshal(rt.sessionMeta(), &meta); err != nil {
		t.Fatal(err)
	}
	text := meta.Antigravity.SystemPrompt.Append
	for _, want := range []string{"Be brief.", "# Skill: wi", "Attach reports with wi attach.", "artifacts folder", "wi attach FILE"} {
		if !strings.Contains(text, want) {
			t.Fatalf("system prompt lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "ignored") || strings.Index(text, "Be brief.") > strings.Index(text, "# Skill: wi") {
		t.Fatalf("system prompt:\n%s", text)
	}
	// Without the operator's instructions or skills, the note still goes.
	rt.request = protocol.LaunchRequest{}
	if !strings.Contains(string(rt.sessionMeta()), "artifacts folder") {
		t.Fatal("no note without instructions")
	}
	// An agy that cannot append gets nothing.
	rt.init.AgentCapabilities.Meta = nil
	if rt.sessionMeta() != nil {
		t.Fatal("appended to an agy that cannot")
	}
}
