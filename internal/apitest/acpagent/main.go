// Command acpagent is a fake Antigravity CLI for tests. `acpagent acp`
// speaks ACP on standard input and output the way `agy acp` does, and a
// prompt's text chooses what it does:
//
//   - "background": a command keeps running after the prompt ends, then
//     the agent replies by itself;
//   - "ask": a fixed-choice question, then "Chose: <answer>";
//   - "permission": a tool permission request, then "Permission: <option>";
//   - "quota": the account's quota is exhausted (stop reason refusal);
//   - "hold": the prompt runs until cancelled, or steered by the next one;
//   - "system": reply with the system prompt addition the session got;
//   - "model": reply with the model and thinking level;
//   - anything else: "Echo: <text>".
//
// Session IDs are fixed per process start count, and ACP_FAKE_LOG names a
// file the agent appends its method calls to.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

var (
	out       = json.NewEncoder(os.Stdout)
	outMu     sync.Mutex
	pendingMu sync.Mutex
	pending   = map[string]chan json.RawMessage{}
	nextID    = 100
	session   string
	system    string
	model     = "gemini-fast"
	thinking  = "high"
	cancelled = make(chan struct{}, 1)
	// steered ends a held prompt when another prompt arrives, as agy
	// steers a running turn with the next prompt.
	steered = make(chan struct{}, 1)
	holding atomic.Bool
	// elicit is set when the client answers forms, which agy then uses
	// for its questions.
	elicit bool
)

func send(m message) {
	m.JSONRPC = "2.0"
	outMu.Lock()
	defer outMu.Unlock()
	_ = out.Encode(m)
}

func reply(id json.RawMessage, result any) {
	data, _ := json.Marshal(result)
	send(message{ID: id, Result: data})
}

func notify(update map[string]any) {
	data, _ := json.Marshal(map[string]any{"sessionId": session, "update": update})
	send(message{Method: "session/update", Params: data})
}

func say(text string) {
	notify(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
}

func usage() { notify(map[string]any{"sessionUpdate": "usage_update", "used": 100, "size": 1000}) }

// request asks the client and waits for its answer.
func request(method string, params any) json.RawMessage {
	data, _ := json.Marshal(params)
	pendingMu.Lock()
	nextID++
	id := fmt.Sprint(nextID)
	ch := make(chan json.RawMessage, 1)
	pending[id] = ch
	pendingMu.Unlock()
	send(message{ID: json.RawMessage(id), Method: method, Params: data})
	return <-ch
}

func logCall(method string) {
	if path := os.Getenv("ACP_FAKE_LOG"); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			fmt.Fprintln(file, method)
			file.Close()
		}
	}
}

func configOptions() []any {
	return []any{
		map[string]any{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": model, "options": []any{map[string]any{"value": "gemini-fast", "name": "Gemini Fast"}, map[string]any{"value": "claude-deep", "name": "Claude Deep"}}},
		map[string]any{"id": "thinking_level", "name": "Thinking", "category": "thought_level", "type": "select", "currentValue": thinking, "options": []any{map[string]any{"value": "low", "name": "Low"}, map[string]any{"value": "high", "name": "High"}}},
	}
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "acp" {
		fmt.Fprintln(os.Stderr, "usage: acpagent acp")
		os.Exit(2)
	}
	if os.Getenv("ACP_FAKE_SIGNED_OUT") != "" {
		fmt.Println("Open the following link to authenticate the ACP server: https://accounts.google.com/o/oauth2/fake")
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var m message
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			continue
		}
		if m.Method == "" {
			pendingMu.Lock()
			ch := pending[string(m.ID)]
			delete(pending, string(m.ID))
			pendingMu.Unlock()
			if ch != nil {
				ch <- m.Result
			}
			continue
		}
		logCall(m.Method)
		switch m.Method {
		case "initialize":
			var params struct {
				ClientCapabilities struct {
					Elicitation json.RawMessage `json:"elicitation"`
				} `json:"clientCapabilities"`
			}
			_ = json.Unmarshal(m.Params, &params)
			elicit = len(params.ClientCapabilities.Elicitation) > 0
			reply(m.ID, map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "Antigravity ACP Server", "version": "1.5.12"},
				"agentCapabilities": map[string]any{"loadSession": true, "promptCapabilities": map[string]any{"image": true}, "sessionCapabilities": map[string]any{"resume": map[string]any{}}, "_meta": map[string]any{"antigravity": map[string]any{"systemPrompt": map[string]any{"append": true}}}},
				"authMethods":       []any{map[string]any{"id": "oauth-personal", "name": "Google"}}})
		case "session/new", "session/resume":
			if os.Getenv("ACP_FAKE_SIGNED_OUT") != "" {
				send(message{ID: m.ID, Error: &struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				}{-32000, "Authentication required"}})
				continue
			}
			var params struct {
				SessionID string `json:"sessionId"`
				Meta      struct {
					Antigravity struct {
						SystemPrompt struct {
							Append string `json:"append"`
						} `json:"systemPrompt"`
					} `json:"antigravity"`
				} `json:"_meta"`
			}
			_ = json.Unmarshal(m.Params, &params)
			system = params.Meta.Antigravity.SystemPrompt.Append
			session = params.SessionID
			if session == "" {
				session = fmt.Sprintf("session-%d", time.Now().UnixNano())
			}
			result := map[string]any{"configOptions": configOptions()}
			if m.Method == "session/new" {
				result["sessionId"] = session
			}
			reply(m.ID, result)
		case "session/set_config_option":
			var params struct{ ConfigID, Value string }
			_ = json.Unmarshal(m.Params, &params)
			if params.ConfigID == "model" {
				model = params.Value
			} else {
				thinking = params.Value
			}
			reply(m.ID, map[string]any{"configOptions": configOptions()})
		case "session/cancel":
			select {
			case cancelled <- struct{}{}:
			default:
			}
		case "session/prompt":
			var params struct {
				Prompt []struct{ Text string } `json:"prompt"`
			}
			_ = json.Unmarshal(m.Params, &params)
			text := ""
			if len(params.Prompt) > 0 {
				text = params.Prompt[0].Text
			}
			go prompt(m.ID, text)
		default:
			if len(m.ID) > 0 {
				send(message{ID: m.ID, Error: &struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				}{-32601, "Method not found"}})
			}
		}
	}
}

func prompt(id json.RawMessage, text string) {
	endTurn := map[string]any{"stopReason": "end_turn"}
	if !strings.Contains(text, "hold") && holding.Load() {
		select {
		case steered <- struct{}{}:
		default:
		}
	}
	switch {
	case strings.Contains(text, "background"):
		notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "cmd-1", "title": "Running run_command", "kind": "execute", "status": "in_progress", "rawInput": map[string]any{"CommandLine": "sleep 1"}})
		say("Started the command.")
		usage()
		reply(id, endTurn)
		time.Sleep(time.Second)
		notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "cmd-1", "status": "completed", "rawOutput": map[string]any{"output": "Command exited with code 0.", "exit_code": 0}})
		say("Background done.")
		usage()
	case strings.Contains(text, "ask") && elicit:
		notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "ask-1", "name": "ask_question", "title": "Run ask_question", "kind": "other", "status": "pending"})
		answer := request("elicitation/create", map[string]any{"sessionId": session, "toolCallId": "ask-1", "mode": "form", "message": "The agent has a question.", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{
			"q1":       map[string]any{"type": "string", "title": "Which color?", "oneOf": []any{map[string]any{"const": "1", "title": "Red"}, map[string]any{"const": "2", "title": "Blue"}}},
			"q1_other": map[string]any{"type": "string", "title": "Other"},
			"q2":       map[string]any{"type": "array", "title": "Which sizes?", "items": map[string]any{"anyOf": []any{map[string]any{"const": "1", "title": "S"}, map[string]any{"const": "2", "title": "M"}, map[string]any{"const": "3", "title": "L"}}}},
			"q2_other": map[string]any{"type": "string", "title": "Other"},
		}}})
		var result struct {
			Action  string
			Content struct {
				Q1      string   `json:"q1"`
				Q1Other string   `json:"q1_other"`
				Q2      []string `json:"q2"`
				Q2Other string   `json:"q2_other"`
			}
		}
		_ = json.Unmarshal(answer, &result)
		color := map[string]string{"1": "Red", "2": "Blue"}[result.Content.Q1]
		if color == "" {
			color = result.Content.Q1Other
		}
		sizes := []string{}
		for _, v := range result.Content.Q2 {
			sizes = append(sizes, map[string]string{"1": "S", "2": "M", "3": "L"}[v])
		}
		if result.Content.Q2Other != "" {
			sizes = append(sizes, result.Content.Q2Other)
		}
		notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "ask-1", "status": "completed"})
		say(fmt.Sprintf("Chose: %s %s (%s)", color, strings.Join(sizes, "+"), result.Action))
		usage()
		reply(id, endTurn)
	case strings.Contains(text, "ask"):
		answer := request("session/request_permission", map[string]any{"sessionId": session, "toolCall": map[string]any{"toolCallId": "interaction_1", "status": "pending", "title": "Which color?", "rawInput": map[string]any{}},
			"options": []any{map[string]any{"optionId": "1", "name": "Red", "kind": "allow_once"}, map[string]any{"optionId": "2", "name": "Blue", "kind": "allow_once"}}})
		var result struct {
			Outcome struct{ Outcome, OptionID string }
		}
		_ = json.Unmarshal(answer, &result)
		chosen := map[string]string{"1": "Red", "2": "Blue"}[result.Outcome.OptionID]
		say("Chose: " + chosen)
		usage()
		reply(id, endTurn)
	case strings.Contains(text, "permission"):
		answer := request("session/request_permission", map[string]any{"sessionId": session, "toolCall": map[string]any{"toolCallId": "tool-7", "title": "Run rm?", "kind": "execute"},
			"options": []any{map[string]any{"optionId": "allow", "name": "Allow", "kind": "allow_once"}, map[string]any{"optionId": "deny", "name": "Deny", "kind": "reject_once"}}})
		var result struct {
			Outcome struct{ Outcome, OptionID string }
		}
		_ = json.Unmarshal(answer, &result)
		say("Permission: " + result.Outcome.OptionID)
		usage()
		reply(id, endTurn)
	case strings.Contains(text, "quota"):
		say("You have exhausted your quota.")
		usage()
		reply(id, map[string]any{"stopReason": "refusal"})
	case strings.Contains(text, "hold"):
		say("Working on it.")
		holding.Store(true)
		defer holding.Store(false)
		select {
		case <-cancelled:
			reply(id, map[string]any{"stopReason": "cancelled"})
		case <-steered:
			reply(id, endTurn)
		}
	case strings.Contains(text, "system"):
		say("System: " + system)
		usage()
		reply(id, endTurn)
	case strings.Contains(text, "model"):
		say("Model: " + model + " " + thinking)
		usage()
		reply(id, endTurn)
	default:
		notify(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "Thinking about it."}})
		notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "read-1", "title": "Running view_file", "kind": "read", "status": "in_progress", "rawInput": map[string]any{"path": "README.md"}})
		notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "read-1", "status": "completed", "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "file contents"}}}})
		say("Echo: " + text)
		usage()
		reply(id, endTurn)
	}
}
