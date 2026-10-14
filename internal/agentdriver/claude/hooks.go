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
	PromptDigest string    `json:"prompt_digest,omitempty"`
	PromptID     string    `json:"prompt_id,omitempty"`
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
		h.PromptDigest = promptDigest(value.Prompt)
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
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "Stop", "StopFailure", "SessionEnd", "MessageDisplay"} {
		hooks[event] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}}}
	}
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
