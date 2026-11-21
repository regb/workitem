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
	if page.Messages[0].Reasoning == nil || page.Messages[0].Reasoning.Text != "hmm" || len(page.Messages[0].Tools) != 1 {
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
	f, ok := fact(3, Record{Type: recordAssistant, StopReason: acp.StopRefusal, Failed: true, Reason: "usage_limit"})
	if !ok || !f.Failed || !f.Terminal || f.StopReason != "usage_limit" {
		t.Fatalf("quota fact: %+v", f)
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

func TestRepliesKeepTextAndToolsInOrder(t *testing.T) {
	tr := &turn{toolIndex: map[string]int{}}
	tr.step().text.WriteString("Let me read it.")
	tr.tools = append(tr.tools, protocol.HistoryTool{ID: "t1", Name: "read_file", Status: "completed"})
	tr.steps[0].tools = append(tr.steps[0].tools, 0)
	tr.step().text.WriteString("\n\nIt says hello.")
	steps := tr.journalSteps()
	if len(steps) != 2 || steps[0].Text != "Let me read it." || len(steps[0].Tools) != 1 || steps[1].Text != "It says hello." || len(steps[1].Tools) != 0 {
		t.Fatalf("steps: %+v", steps)
	}
	messages := assistantMessages(Record{Type: recordAssistant, ID: "a1", Text: "Let me read it.\n\nIt says hello.", Tools: tr.tools, Steps: steps, StopReason: acp.StopCancelled})
	if len(messages) != 2 || messages[0].ID != "a1.0" || messages[0].Aborted || len(messages[0].Tools) != 1 || messages[1].ID != "a1" || !messages[1].Aborted || messages[1].Text != "It says hello." {
		t.Fatalf("messages: %+v", messages)
	}
	if single := (&turn{steps: []turnStep{{}}}).journalSteps(); single != nil {
		t.Fatalf("a single step is the record itself: %+v", single)
	}
}
