package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

type hookInput struct {
	Event        string    `json:"hook_event_name"`
	SessionID    string    `json:"session_id"`
	Transcript   string    `json:"transcript_path,omitempty"`
	Tool         string    `json:"tool_name,omitempty"`
	Notification string    `json:"notification_type,omitempty"`
	PromptDigest string    `json:"prompt_digest,omitempty"`
	PromptID     string    `json:"prompt_id,omitempty"`
	Model        string    `json:"model,omitempty"`
	ObservedAt   time.Time `json:"observed_at,omitempty"`
}

func hookEvent(event string) (string, bool) {
	switch event {
	case "SessionStart":
		// SessionStart precedes rendering. Readiness is reported after inspection.
		return "", false
	case "UserPromptSubmit":
		// Receipt precedes user hooks that can still block the prompt.
		return "", false
	case "PreToolUse":
		return protocol.EventToolStarted, false
	case "PostToolUse":
		return protocol.EventToolCompleted, false
	case "PostToolUseFailure":
		return protocol.EventToolCompleted, true
	case "Stop":
		return protocol.EventAgentSettled, false
	case "StopFailure":
		return protocol.EventAgentSettled, true
	case "SessionEnd":
		return protocol.EventRuntimeStopping, false
	}
	return "", false
}

// RunHook forwards only compact lifecycle metadata to the semantic hook endpoint.
// MessageDisplay text uses a separate private ephemeral endpoint. Prompts, tool
// arguments/output and native errors never enter either metadata or retained events.
// Hook settings are merged with the user's normal Claude hooks.
func RunHook(ctx context.Context, path string, input io.Reader) error {
	var value struct {
		hookInput
		Prompt string `json:"prompt"`
		displayFields
	}
	if err := json.NewDecoder(io.LimitReader(input, 4<<20)).Decode(&value); err != nil {
		return err
	}
	h := value.hookInput
	h.PromptDigest = ""
	h.ObservedAt = time.Now().UTC()
	if h.Event == "MessageDisplay" {
		return submitDisplay(ctx, path, h, value.displayFields)
	}
	if h.Event == "UserPromptSubmit" && value.Prompt != "" {
		h.PromptDigest = promptDigest(unpasted(value.Prompt))
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return protocol.SubmitControl(ctx, path, protocol.ControlCommand{ProtocolVersion: protocol.RuntimeVersion, ID: fmt.Sprintf("hook-%d", time.Now().UnixNano()), Type: "hook", Message: string(data)})
}
func privateSocketPath(path, label string) string {
	digest := sha256.Sum256([]byte(label + ":" + filepath.Base(path)))
	return filepath.Join(filepath.Dir(path), hex.EncodeToString(digest[:8])+".sock")
}
func hookSocketPath(control string) string { return privateSocketPath(control, "claude-hooks") }
func quote(s string) string                { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func hookSettings(self, socket string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "wi-claude-hooks-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	hooks := map[string]any{}
	command := quote(self) + " internal-driver claude hook " + quote(socket)
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "Stop", "StopFailure", "SessionEnd", "MessageDisplay", "Notification"} {
		hooks[event] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}}}
	}
	// A question waits for an answer through wi for as long as the operator
	// takes, unless the terminal is attached and Claude shows its dialog.
	ask := quote(self) + " internal-driver claude ask " + quote(socket)
	hooks["PreToolUse"] = append(hooks["PreToolUse"].([]any), map[string]any{"matcher": "AskUserQuestion", "hooks": []any{map[string]any{"type": "command", "command": ask, "timeout": askHookTimeout}}})
	data, err := json.Marshal(map[string]any{"hooks": hooks})
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}
func prepareStorage(path, session string, extra map[string]string) ([]string, string, error) {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for k, v := range extra {
		env[k] = v
	}
	root := env["CLAUDE_CONFIG_DIR"]
	if root == "" {
		root = filepath.Join(env["HOME"], ".claude")
	}
	if !filepath.IsAbs(root) {
		return nil, "", fmt.Errorf("Claude configuration directory must be absolute")
	}
	namespace := "wi-" + session
	project := filepath.Join(root, "projects", namespace)
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(project), 0700); err != nil {
		return nil, "", err
	}
	if target, err := os.Readlink(project); err == nil {
		if target != directory {
			return nil, "", fmt.Errorf("Claude transcript namespace belongs to another directory")
		}
	} else if os.IsNotExist(err) {
		if err := os.Symlink(directory, project); err != nil {
			return nil, "", err
		}
	} else {
		return nil, "", fmt.Errorf("refusing to replace Claude transcript namespace: %w", err)
	}
	env["CLAUDE_CONFIG_DIR"] = root
	env["CLAUDE_CODE_PROJECT_DIR_NAME"] = namespace
	delete(env, "CLAUDECODE")
	delete(env, "CLAUDE_CODE_SESSION_ID")
	delete(env, protocol.BootstrapSocketEnv)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(env))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out, filepath.Join(project, session+".jsonl"), nil
}
func exportSkills(skills []protocol.Skill) (string, func(), error) {
	if err := protocol.ValidateSkills(skills); err != nil {
		return "", func() {}, err
	}
	if len(skills) == 0 {
		return "", func() {}, nil
	}
	dir, err := os.MkdirTemp("", "wi-claude-skills-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	if err := os.Mkdir(filepath.Join(dir, ".claude-plugin"), 0700); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(`{"name":"wi-driver","version":"1.0.0"}`), 0600); err != nil {
		cleanup()
		return "", func() {}, err
	}
	for _, skill := range skills {
		for _, file := range skill.Files {
			p := filepath.Join(dir, "skills", skill.Name, filepath.FromSlash(file.Path))
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				cleanup()
				return "", func() {}, err
			}
			mode := os.FileMode(0600)
			if file.Executable {
				mode = 0700
			}
			if err := os.WriteFile(p, file.Data, mode); err != nil {
				cleanup()
				return "", func() {}, err
			}
		}
	}
	return dir, cleanup, nil
}

// askHookTimeout bounds how long Claude lets the question hook wait for an
// answer, in seconds.
const askHookTimeout = 7 * 24 * 60 * 60

// askInput is what the question hook sends its runtime: the question Claude
// asks, to answer through CommandAnswer.
type askInput struct {
	SessionID string         `json:"session_id"`
	ToolUseID string         `json:"tool_use_id"`
	Input     map[string]any `json:"tool_input"`
}

// askResult is the runtime's reply to the question hook: answers, or none
// when Claude should show its own dialog.
type askResult struct {
	Answers map[string]string `json:"answers,omitempty"`
}

// RunAskHook is Claude's PreToolUse hook for AskUserQuestion. It waits for
// the runtime to answer the question and returns the answers as the tool's
// input, so Claude takes them as the user's. Without answers, or on any
// failure, it returns nothing and Claude shows its dialog as usual.
func RunAskHook(ctx context.Context, path string, input io.Reader, output io.Writer) {
	var value askInput
	if json.NewDecoder(io.LimitReader(input, 1<<20)).Decode(&value) != nil || len(value.ToolUseID) > 200 {
		return
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > protocol.MaxControlMessageBytes {
		return
	}
	result, err := protocol.SubmitControlResult(ctx, path, protocol.ControlCommand{ProtocolVersion: protocol.RuntimeVersion, ID: fmt.Sprintf("ask-%d", time.Now().UnixNano()), Type: "ask", Message: string(data)})
	if err != nil || result == "" {
		return
	}
	var answered askResult
	if json.Unmarshal([]byte(result), &answered) != nil || len(answered.Answers) == 0 {
		return
	}
	updated := make(map[string]any, len(value.Input)+1)
	for key, field := range value.Input {
		updated[key] = field
	}
	updated["answers"] = answered.Answers
	_ = json.NewEncoder(output).Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "allow", "updatedInput": updated}})
}
