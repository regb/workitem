package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestInvokeIsToolFreeAndNonPersisted(t *testing.T) {
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args")
	inputLog := filepath.Join(dir, "input")
	t.Setenv("WI_ID", "must-not-leak")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "must-not-leak")
	path := filepath.Join(dir, "fake-pi")
	script := "#!/bin/sh\nset -eu\n[ -z \"${WI_ID:-}${PI_CODING_AGENT_SESSION_DIR:-}\" ]\nprintf '%s\\n' \"$@\" > '" + argsLog + "'\ncat > '" + inputLog + "'\nprintf 'Fix failing authentication tests\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	answer, err := Invoke(context.Background(), Config{Executable: path, Model: "anthropic/haiku"}, driverprotocol.ModelInvokeRequest{Instructions: "Title it.", Prompt: "Untrusted @file --flags task"})
	if err != nil || answer != "Fix failing authentication tests" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	args, _ := os.ReadFile(argsLog)
	input, _ := os.ReadFile(inputLog)
	for _, flag := range []string{"--print", "--no-session", "--no-tools", "--no-extensions", "--no-context-files", "--no-approve", "--thinking\noff", "--system-prompt\nTitle it.", "--model\nanthropic/haiku"} {
		if !strings.Contains(string(args), flag+"\n") {
			t.Fatalf("missing %s: %s", flag, args)
		}
	}
	if strings.Contains(string(args), "Untrusted") || string(input) != "Untrusted @file --flags task" {
		t.Fatal("prompt interpreted as command-line options instead of stdin")
	}
}
