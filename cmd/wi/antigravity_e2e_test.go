package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestAntigravityDriverEndToEnd runs the bundled Antigravity driver against
// a fake `agy acp`: prompts, live settings, background work that outlives
// its prompt, questions, full-access permissions, steering, cancel, quota,
// and resuming the conversation in a new runtime.
func TestAntigravityDriverEndToEnd(t *testing.T) {
	f := newBinaryFixture(t)
	ctx := context.Background()
	agent := apitest.Command(t, "./internal/apitest/acpagent")
	callLog := filepath.Join(f.root, "acp-calls.log")
	f.env = append(f.env, "ACP_FAKE_LOG="+callLog)
	// The daemon launches drivers with its own environment: restart it.
	f.run("daemon", "stop")
	f.run("daemon", "start")

	profiles, err := f.clients.Profiles.ListAgentProfiles(ctx, connect.NewRequest(&v1.ListAgentProfilesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	config, _ := structpb.NewStruct(map[string]any{"executable": agent})
	if _, err := f.clients.Profiles.PutAgentProfile(ctx, connect.NewRequest(&v1.PutAgentProfileRequest{Profile: &v1.AgentProfile{Name: "agy", Driver: "antigravity", Config: config}, ExpectedRevision: profiles.Msg.Revision})); err != nil {
		t.Fatal(err)
	}
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	var started v1.CreateAndStartItemResponse
	f.json(&started, "new", "--repo", repo.Repository.Id, "--title", "Antigravity", "--agent", "agy", "--start", "--agent-mode", "headless", "--prompt", "hello there")
	item := started.Item.Id

	history := func() []*v1.ConversationMessage {
		t.Helper()
		value, err := f.clients.Agents.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Messages
	}
	// lastReply is the text of the latest reply.
	lastReply := func() string {
		messages := history()
		if len(messages) == 0 || messages[len(messages)-1].Role != v1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			return ""
		}
		return messageText(messages[len(messages)-1])
	}
	waitReply := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !strings.Contains(lastReply(), want); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("no reply %q; history %v", want, history())
			}
		}
	}
	agentState := func() *v1.Agent {
		value, err := f.clients.Agents.GetAgent(ctx, connect.NewRequest(&v1.GetAgentRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Agent
	}
	send := func(text string, delivery v1.Delivery) {
		t.Helper()
		if _, err := f.clients.Agents.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: item, Text: text, Delivery: delivery})); err != nil {
			t.Fatalf("send %q: %v", text, err)
		}
	}

	waitReply("Echo: hello there")
	f.idle(item)
	// The reply is one message in the order it was written: reasoning, the
	// tool it called, then the text after it.
	messages := history()
	reply := messages[len(messages)-1]
	if parts := reply.Parts; reply.Role != v1.MessageRole_MESSAGE_ROLE_ASSISTANT || len(parts) != 3 ||
		parts[0].GetThought().GetText() != "Thinking about it." ||
		parts[1].GetTool().GetStatus() != v1.ToolCallStatus_TOOL_CALL_STATUS_COMPLETED || parts[1].GetTool().GetOutput() != "file contents" ||
		parts[2].GetText() != "Echo: hello there" {
		t.Fatalf("reply out of order: %v", reply)
	}
	if capabilities := agentState().Capabilities; capabilities == nil || !capabilities.Answer || !capabilities.Steering || len(capabilities.LiveSettings) != 2 {
		t.Fatalf("capabilities: %v", capabilities)
	}

	// A message sent with a client ID comes back in history marked with it:
	// the driver links the prompt to its entry, and the daemon keeps it.
	if _, err := f.clients.Agents.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: item, Text: "linked", Delivery: v1.Delivery_DELIVERY_PROMPT, ClientMessageId: "page-1"})); err != nil {
		t.Fatal(err)
	}
	waitReply("Echo: linked")
	f.idle(item)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		linked := ""
		for _, m := range history() {
			if m.Role == v1.MessageRole_MESSAGE_ROLE_USER && messageText(m) == "linked" {
				linked = m.ClientMessageId
			}
		}
		if linked == "page-1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sent message not linked in history: %q", linked)
		}
	}

	// wi's instructions reach the agent's system prompt: the wi skill, which
	// ACP has no other way to pass, and where deliverables go.
	send("system", v1.Delivery_DELIVERY_PROMPT)
	waitReply("System: ")
	if system := lastReply(); !strings.Contains(system, "# Skill: wi") || !strings.Contains(system, "wi attach FILE") {
		t.Fatalf("system prompt addition: %q", system)
	}
	f.idle(item)

	// Model and reasoning change live.
	f.run("agent", "settings", "set", "--item", item, "model=claude-deep", "reasoning=medium")
	send("model", v1.Delivery_DELIVERY_PROMPT)
	waitReply("Model: claude-deep low")
	f.idle(item)

	// A command outliving its prompt keeps the agent working until it
	// replies by itself.
	send("background", v1.Delivery_DELIVERY_PROMPT)
	time.Sleep(500 * time.Millisecond)
	if status := agentState().Status; status != v1.AgentStatus_AGENT_STATUS_BUSY {
		t.Fatalf("agent settled with a command running: %v", status)
	}
	waitReply("Background done.")
	if !strings.Contains(lastReply(), "Started the command.\n\nBackground done.") {
		t.Fatalf("replies of one turn: %q", lastReply())
	}
	f.idle(item)

	// Tools are allowed once each.
	send("permission", v1.Delivery_DELIVERY_PROMPT)
	waitReply("Permission: allow")
	f.idle(item)

	// A question waits for the operator's answer.
	send("ask", v1.Delivery_DELIVERY_PROMPT)
	for deadline := time.Now().Add(10 * time.Second); !agentState().Asking; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no question")
		}
	}
	shown := string(f.run("agent", "answer", "--item", item))
	if !strings.Contains(shown, "Which color?") || !strings.Contains(shown, "2) Blue") || !strings.Contains(shown, "Which sizes? (several") {
		t.Fatalf("question: %s", shown)
	}
	f.run("agent", "answer", "--item", item, "Green", "1,3,XL")
	waitReply("Chose: Green S+L+XL (accept)")
	f.idle(item)

	// A message during a turn steers it.
	send("hold on", v1.Delivery_DELIVERY_PROMPT)
	time.Sleep(300 * time.Millisecond)
	send("steered", v1.Delivery_DELIVERY_STEER)
	waitReply("Echo: steered")
	if !strings.Contains(lastReply(), "Working on it.") {
		t.Fatalf("steered turn: %q", lastReply())
	}
	f.idle(item)

	// Stop cancels the turn.
	send("hold again", v1.Delivery_DELIVERY_PROMPT)
	time.Sleep(300 * time.Millisecond)
	if _, err := f.clients.Agents.AbortTurn(ctx, connect.NewRequest(&v1.AbortTurnRequest{ItemId: item})); err != nil {
		t.Fatal(err)
	}
	waitReply("Working on it.")
	if messages := history(); !messages[len(messages)-1].Aborted {
		t.Fatalf("stopped turn: %v", messages[len(messages)-1])
	}
	f.idle(item)
	// Each turn is one reply in history.
	for i, m := range history() {
		if m.Role == v1.MessageRole_MESSAGE_ROLE_ASSISTANT && len(m.Parts) == 0 {
			t.Fatalf("empty reply %d: %v", i, history())
		}
	}

	// An exhausted quota is a usage limit: the item waits on Antigravity.
	send("quota", v1.Delivery_DELIVERY_PROMPT)
	waitReply("exhausted your quota")
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		value, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		if blockers := value.Msg.Item.Blockers; len(blockers) == 1 && blockers[0].Kind == v1.BlockerKind_BLOCKER_KIND_USAGE {
			if blockers[0].Provider != "antigravity" {
				t.Fatalf("usage blocker: %v", blockers[0])
			}
			f.run("blocker", "clear", "--all", "--drop-follow-up", item)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no usage blocker: %v", value.Msg.Item.Blockers)
		}
	}

	// A new runtime resumes the native session; history stays.
	before := len(history())
	if _, err := f.clients.Agents.StopAgent(ctx, connect.NewRequest(&v1.StopAgentRequest{ItemId: item})); err != nil {
		t.Fatal(err)
	}
	send("after restart", v1.Delivery_DELIVERY_PROMPT)
	waitReply("Echo: after restart")
	// The prompt, then the reply, one message with its tool call.
	if after := len(history()); after != before+2 {
		t.Fatalf("history across restart: %d then %d messages", before, after)
	}
	calls, _ := os.ReadFile(callLog)
	if !strings.Contains(string(calls), "session/resume") || strings.Count(string(calls), "session/new") != 1 {
		t.Fatalf("calls: %s", calls)
	}
}
