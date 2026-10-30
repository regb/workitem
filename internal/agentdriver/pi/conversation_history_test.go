package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
)

func TestHistorySelectsBranchAndIgnoresIncompleteAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.jsonl")
	data := `{"type":"session","version":3,"id":"session"}
{"type":"message","id":"root","parentId":null,"message":{"role":"user","content":"hello"}}
{"type":"message","id":"abandoned","parentId":"root","message":{"role":"assistant","content":[{"type":"text","text":"old answer"}]}}
{"type":"message","id":"active","parentId":"root","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private"},{"type":"text","text":"new answer"},{"type":"toolCall","name":"bash"}]}}
{"type":"message","id":"unfinished"`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadHistory(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 || result.LeafID != "active" || strings.Contains(result.Messages[1].Text, "old answer") || strings.Contains(result.Messages[1].Text, "private") {
		t.Fatalf("branch %+v", result)
	}
	result, err = ReadHistory(context.Background(), path, "abandoned")
	if err != nil || len(result.Messages) != 2 || result.Messages[1].Text != "old answer" {
		t.Fatalf("live TUI leaf %+v %v", result, err)
	}
	if _, err := ReadHistory(context.Background(), path, "missing"); err == nil {
		t.Fatal("missing leaf accepted")
	}
}
func TestHistoryJoinsToolCallAndResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.jsonl")
	data := `{"type":"session","version":3,"id":"session"}
{"type":"message","id":"user","parentId":null,"message":{"role":"user","content":[{"type":"text","text":"inspect"},{"type":"image","mimeType":"image/png","data":"iVBORw0KGgo="}]}}
{"type":"message","id":"call","parentId":"user","message":{"role":"assistant","content":[{"type":"text","text":"I'll check."},{"type":"toolCall","id":"tool-1","name":"bash","arguments":{"command":"ls"}}]}}
{"type":"message","id":"result","parentId":"call","message":{"role":"toolResult","toolCallId":"tool-1","toolName":"bash","content":[{"type":"text","text":"README.md"}]}}
{"type":"message","id":"answer","parentId":"result","message":{"role":"assistant","content":[{"type":"text","text":"The repo has a README."}]}}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadHistory(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("messages = %+v", result.Messages)
	}
	if len(result.Messages[0].Images) != 1 || result.Messages[0].Images[0].MIMEType != "image/png" {
		t.Fatalf("images = %+v", result.Messages[0].Images)
	}
	tool := result.Messages[1].Tools
	if len(tool) != 1 || tool[0].Name != "bash" || tool[0].Status != "completed" || tool[0].Output != "README.md" || !strings.Contains(tool[0].Input, `"ls"`) {
		t.Fatalf("tool = %+v", tool)
	}
	for _, message := range result.Messages {
		if message.Role == "toolResult" {
			t.Fatalf("tool result rendered as a second message: %+v", message)
		}
	}
}

func TestHistoryRejectsMalformedAndCyclicInput(t *testing.T) {
	for _, data := range []string{"not json\n", `{"type":"message","id":"cycle","parentId":"cycle"}` + "\n", `{"type":"message","message":{"role":"user","content":"legacy"}}` + "\n"} {
		path := filepath.Join(t.TempDir(), "native.jsonl")
		_ = os.WriteFile(path, []byte(data), 0600)
		if _, err := ReadHistory(context.Background(), path, ""); err == nil {
			t.Fatal("malformed history accepted")
		}
	}
}
func TestHistoryLimitsAndPublicOperation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(f)
	parent := ""
	for i := 0; i < 250; i++ {
		id := strings.Repeat("x", i+1)
		_ = encoder.Encode(map[string]any{"type": "message", "id": id, "parentId": parent, "message": map[string]any{"role": "user", "content": "hello"}})
		parent = id
	}
	_ = f.Close()
	response, err := handleConversationOperation(context.Background(), driverprotocol.Request{Operation: driverprotocol.OperationConversationHistory, Conversation: &driverprotocol.ConversationRequest{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	if response.History == nil || !response.History.Truncated || len(response.History.Messages) != 200 {
		t.Fatalf("history limit %+v", response.History)
	}
}
func TestNativeRPCIdleGuardRefusesBeforeWritingOrStopping(t *testing.T) {
	transport := &nativeRPCTransport{spec: ExecSpec{RuntimeID: "owner", WorkItemID: "item"}, busy: true, handled: map[string]bool{}}
	for _, typ := range []string{agent.CommandPrompt, agent.CommandShutdown} {
		err := transport.sendCommand(agent.ControlCommand{ProtocolVersion: 1, ID: typ, RuntimeID: "owner", WorkItemID: "item", Type: typ, RequireIdle: true})
		if err == nil {
			t.Fatal("busy control accepted")
		}
		if transport.stopping || transport.handled[typ] {
			t.Fatal("refused command changed runtime")
		}
	}
}

// A reply the operator stopped keeps its partial text, marked aborted.
func TestHistoryMarksAnAbortedReply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.jsonl")
	data := `{"type":"session","version":3,"id":"session"}
{"type":"message","id":"root","parentId":null,"message":{"role":"user","content":"hello"}}
{"type":"message","id":"stopped","parentId":"root","message":{"role":"assistant","stopReason":"aborted","content":[{"type":"text","text":"partial"}]}}
{"type":"message","id":"again","parentId":"stopped","message":{"role":"user","content":"go on"}}
{"type":"message","id":"done","parentId":"again","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"complete"}]}}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadHistory(context.Background(), path, "")
	if err != nil || len(result.Messages) != 4 {
		t.Fatalf("%+v %v", result, err)
	}
	if stopped := result.Messages[1]; !stopped.Aborted || stopped.Text != "partial" || result.Messages[3].Aborted {
		t.Fatalf("aborted marks: %+v", result.Messages)
	}
}

func TestHistoryShowsAnOperatorCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.jsonl")
	data := `{"type":"session","version":3,"id":"session"}
{"type":"message","id":"ran","parentId":null,"message":{"role":"bashExecution","command":"ls","output":"README.md\n","exitCode":0,"cancelled":false}}
{"type":"message","id":"broke","parentId":"ran","message":{"role":"bashExecution","command":"false","output":"","exitCode":1,"cancelled":false}}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadHistory(context.Background(), path, "")
	if err != nil || len(result.Messages) != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	ran := result.Messages[0]
	if ran.Role != "user" || len(ran.Tools) != 1 || ran.Tools[0].Name != "bash" || ran.Tools[0].Input != `{"command":"ls"}` || ran.Tools[0].Output != "README.md\n" || ran.Tools[0].Status != "completed" {
		t.Fatalf("command: %+v", ran)
	}
	if broke := result.Messages[1].Tools[0]; broke.Status != "failed" {
		t.Fatalf("failed command: %+v", broke)
	}
}
