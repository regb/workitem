package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestTitleGenerationIsToolFreeAndNonPersisted(t *testing.T) {
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args")
	inputLog := filepath.Join(dir, "input")
	t.Setenv("WI_ID", "must-not-leak")
	t.Setenv("WI_AGENT_RUNTIME_ID", "must-not-leak")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "must-not-leak")
	path := filepath.Join(dir, "fake-pi")
	script := "#!/bin/sh\nset -eu\n[ -z \"${WI_ID:-}${WI_AGENT_RUNTIME_ID:-}${PI_CODING_AGENT_SESSION_DIR:-}\" ]\nprintf '%s\\n' \"$@\" > '" + argsLog + "'\ncat > '" + inputLog + "'\nprintf '\"Fix failing authentication tests\"\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	title, err := GenerateTitle(context.Background(), Config{Executable: path}, "Untrusted @file --flags task")
	if err != nil || title != "Fix failing authentication tests" {
		t.Fatalf("title=%q err=%v", title, err)
	}
	args, _ := os.ReadFile(argsLog)
	input, _ := os.ReadFile(inputLog)
	for _, flag := range []string{"--print", "--no-session", "--no-tools", "--no-extensions", "--no-context-files", "--no-approve"} {
		if !strings.Contains(string(args), flag+"\n") {
			t.Fatalf("missing %s: %s", flag, args)
		}
	}
	if strings.Contains(string(args), "Untrusted") || string(input) != driverprotocol.TitleRequest("Untrusted @file --flags task") {
		t.Fatal("task interpreted as command-line options instead of stdin")
	}
}
func TestTitleOutputAndCancellation(t *testing.T) {
	for _, value := range []string{"", "line one\nline two", "bad\x1btitle", strings.Repeat("x", 5000)} {
		path := filepath.Join(t.TempDir(), "fake-pi")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' '"+value+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := GenerateTitle(context.Background(), Config{Executable: path}, "task"); err == nil {
			t.Fatalf("invalid output accepted: %q", value)
		}
	}
	path := filepath.Join(t.TempDir(), "fake-pi")
	os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 10\n"), 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := GenerateTitle(ctx, Config{Executable: path}, "task"); err == nil {
		t.Fatal("cancelled title generation succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("title generation ignored cancellation")
	}
}
