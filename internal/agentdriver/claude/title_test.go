package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestTitleRequestIsolation(t *testing.T) {
	dir := t.TempDir()
	backend := filepath.Join(dir, "claude")
	script := `#!/bin/sh
[ -z "$WI_ID" ] && [ -z "$WI_AGENT_RUNTIME_ID" ] && [ -z "$TMUX" ] && [ -z "$CLAUDE_CODE_PROJECT_DIR_NAME" ] || exit 10
[ "$PWD" != "$TITLE_ORIGINAL_CWD" ] || exit 11
printf '%s\n' "$@" > "$TITLE_ARGS"
cat > "$TITLE_INPUT"
printf '"Add API retry handling"\n'
`
	if err := os.WriteFile(backend, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	t.Setenv("TITLE_ORIGINAL_CWD", cwd)
	t.Setenv("TITLE_ARGS", filepath.Join(dir, "args"))
	t.Setenv("TITLE_INPUT", filepath.Join(dir, "input"))
	t.Setenv("WI_ID", "item")
	t.Setenv("WI_AGENT_RUNTIME_ID", "runtime")
	t.Setenv("TMUX", "tmux")
	t.Setenv("CLAUDE_CODE_PROJECT_DIR_NAME", "item-native")
	title, err := GenerateTitle(context.Background(), Config{Executable: backend, Model: "sonnet"}, "task text")
	if err != nil || title != "Add API retry handling" {
		t.Fatalf("%q %v", title, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "args"))
	args := string(data)
	for _, flag := range []string{"--print", "--dangerously-skip-permissions", "--no-session-persistence", "--tools\n\n", "--disable-slash-commands", "--strict-mcp-config", "--settings", "--model\nsonnet"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("missing %q", flag)
		}
	}
	input, _ := os.ReadFile(filepath.Join(dir, "input"))
	if string(input) != protocol.TitleRequest("task text") {
		t.Fatalf("input %q", input)
	}
}
func TestTitleRejectsInvalidOutput(t *testing.T) {
	for _, command := range []string{`printf ''`, `printf 'bad\ntitle\n'`, `exit 1`, `i=0; while [ "$i" -lt 5000 ]; do printf x; i=$((i+1)); done`} {
		path := filepath.Join(t.TempDir(), "claude")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+command+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := GenerateTitle(context.Background(), Config{Executable: path}, "task"); err == nil {
			t.Fatal("accepted invalid output")
		}
	}
}
