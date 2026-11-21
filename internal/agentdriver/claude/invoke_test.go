package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestInvokeIsABareModelCall(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, "claude")
	script := `#!/bin/sh
[ -z "$WI_ID" ] && [ -z "$WI_AGENT_RUNTIME_ID" ] && [ -z "$TMUX" ] && [ -z "$CLAUDE_CODE_PROJECT_DIR_NAME" ] || exit 10
[ "$PWD" != "$INVOKE_ORIGINAL_CWD" ] || exit 11
printf '%s\n' "$@" > "$INVOKE_ARGS"
cat > "$INVOKE_INPUT"
printf 'Add API retry handling\n'
`
	if err := os.WriteFile(backend, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	t.Setenv("INVOKE_ORIGINAL_CWD", cwd)
	t.Setenv("INVOKE_ARGS", filepath.Join(dir, "args"))
	t.Setenv("INVOKE_INPUT", filepath.Join(dir, "input"))
	t.Setenv("WI_ID", "item")
	t.Setenv("WI_AGENT_RUNTIME_ID", "runtime")
	t.Setenv("TMUX", "tmux")
	t.Setenv("CLAUDE_CODE_PROJECT_DIR_NAME", "item-native")
	answer, err := Invoke(context.Background(), Config{Executable: backend, Model: "haiku", Reasoning: "low"}, protocol.ModelInvokeRequest{Instructions: "Title it.", Prompt: "--task text"})
	if err != nil || answer != "Add API retry handling" {
		t.Fatalf("%q %v", answer, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "args"))
	args := string(data)
	for _, flag := range []string{"--print", "--no-session-persistence", "--tools\n\n", "--disable-slash-commands", "--strict-mcp-config", "--settings", "--system-prompt\nTitle it.\n", "--model\nhaiku", "--effort\nlow"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("missing %q in\n%s", flag, args)
		}
	}
	if input, _ := os.ReadFile(filepath.Join(dir, "input")); string(input) != "--task text" || strings.Contains(args, "--task text") {
		t.Fatalf("the prompt goes on stdin: %q", input)
	}
	if _, err := Invoke(context.Background(), Config{Executable: backend}, protocol.ModelInvokeRequest{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "args")); !strings.Contains(string(data), "--system-prompt\n"+neutralInstructions+"\n") {
		t.Fatalf("no neutral instructions:\n%s", data)
	}
}
