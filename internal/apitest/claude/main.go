// A fake Claude process used only by isolated real-binary startup tests.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func headless(root string) {
	enc := json.NewEncoder(os.Stdout)
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var frame struct {
			Type, UUID string
			RequestID  string `json:"request_id"`
			Message    struct{ Content []struct{ Text string } }
		}
		if err := json.Unmarshal(scan.Bytes(), &frame); err != nil {
			panic(err)
		}
		switch frame.Type {
		case "control_request":
			_ = enc.Encode(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": frame.RequestID, "response": map[string]any{"claude_code_version": "2.1.299", "session_state": "idle"}}})
		case "user":
			for {
				if _, err := os.Stat(filepath.Join(root, "release")); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			file, err := os.OpenFile(filepath.Join(root, "prompts"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				panic(err)
			}
			_ = json.NewEncoder(file).Encode(frame.Message.Content[0].Text)
			_ = file.Close()
			_ = enc.Encode(map[string]any{"type": "system", "subtype": "init", "capabilities": []string{"msg_lifecycle_v1", "request_marker_lists_v1", "interrupt_receipt_v1", "interrupt_cancel_queued_v1"}})
			_ = enc.Encode(map[string]any{"type": "command_lifecycle", "state": "queued", "command_uuid": frame.UUID})
			_ = enc.Encode(map[string]any{"type": "user", "uuid": frame.UUID, "isReplay": true})
			_ = enc.Encode(map[string]any{"type": "result", "subtype": "success", "num_turns": 1, "user_message_uuids": []string{frame.UUID}})
		}
	}
}

func main() {
	value := func(flag string) string {
		for i, arg := range os.Args {
			if arg == flag && i+1 < len(os.Args) {
				return os.Args[i+1]
			}
		}
		return ""
	}
	root := os.Getenv("WI_FAKE_CLAUDE_ROOT")
	if root == "" {
		panic("isolated fixture root required")
	}
	if value("--input-format") == "stream-json" {
		headless(root)
		return
	}
	var settings struct {
		Hooks map[string][]struct{ Hooks []struct{ Command string } }
	}
	data, err := os.ReadFile(value("--settings"))
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		panic(err)
	}
	session := value("--session-id")
	if session == "" {
		session = value("--resume")
	}
	native := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", os.Getenv("CLAUDE_CODE_PROJECT_DIR_NAME"), session+".jsonl")
	// Give filesystem observations the same idle fact as the fake UI. An
	// empty transcript is unknown and may conservatively be ranked busy.
	idle, _ := json.Marshal(map[string]any{"type": "assistant", "uuid": "fixture-idle", "sessionId": session, "timestamp": time.Now().UTC(), "message": map[string]any{"role": "assistant", "stop_reason": "end_turn", "content": []any{map[string]string{"type": "text", "text": "Fixture ready"}}}})
	if err := os.WriteFile(native, append(idle, '\n'), 0600); err != nil {
		panic(err)
	}
	hook := func(event, prompt string) {
		data, _ := json.Marshal(map[string]string{"hook_event_name": event, "session_id": session, "transcript_path": native, "prompt": prompt})
		cmd := exec.Command("sh", "-c", settings.Hooks[event][0].Hooks[0].Command)
		cmd.Stdin = strings.NewReader(string(data))
		if out, err := cmd.CombinedOutput(); err != nil {
			panic(fmt.Sprintf("hook: %v %s", err, out))
		}
	}
	raw := exec.Command("stty", "-echo", "-icanon", "min", "1", "time", "0")
	raw.Stdin = os.Stdin
	if err := raw.Run(); err != nil {
		panic(err)
	}
	render := func(text string) {
		fmt.Printf("\x1b[2J\x1b[H───\r\n❯ %s\r\n───\r\n", strings.ReplaceAll(text, "\n", "\r\n  "))
	}
	fmt.Print("\x1b[?2004h")
	render("")
	for {
		if _, err := os.Stat(filepath.Join(root, "release")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	hook("SessionStart", "")
	reader := bufio.NewReader(os.Stdin)
	draft := ""
	paste := false
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return
		}
		if b == 27 {
			seq := make([]byte, 5)
			for i := range seq {
				seq[i], err = reader.ReadByte()
				if err != nil {
					return
				}
			}
			switch string(seq) {
			case "[200~":
				paste = true
			case "[201~":
				paste = false
			}
			render(draft)
			continue
		}
		if (b == '\r' || b == '\n') && !paste {
			file, err := os.OpenFile(filepath.Join(root, "prompts"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				panic(err)
			}
			_ = json.NewEncoder(file).Encode(draft)
			_ = file.Close()
			hook("UserPromptSubmit", draft)
			hook("Stop", "")
			draft = ""
			render("")
		} else {
			draft += string(b)
			render(draft)
		}
	}
}
