package pi_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/pi"
)

func TestForkSessionRehomesHeaderAndPreservesEntries(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.jsonl")
	target := filepath.Join(root, "target.jsonl")
	entry := `{"type":"message","id":"abcd1234","parentId":null,"timestamp":"2026-08-14T09:00:00Z","message":{"role":"user","content":"hello"}}`
	content := `{"type":"session","version":3,"id":"11111111-1111-4111-8111-111111111111","timestamp":"2026-08-03T07:48:31.948Z","cwd":"/worktrees/slot-0004"}` + "\n" + entry + "\n"
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	client := pi.New("pi")
	if cwd, err := client.SessionCWD(source); err != nil || cwd != "/worktrees/slot-0004" {
		t.Fatalf("cwd=%q err=%v", cwd, err)
	}
	if err := client.ForkSession(context.Background(), source, target, "/worktrees/slot-0002"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[1] != entry {
		t.Fatalf("forked lines = %#v", lines)
	}
	var header struct {
		Type, ID, CWD, ParentSession string
		Version                      int
	}
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatal(err)
	}
	absoluteSource, _ := filepath.Abs(source)
	if header.Type != "session" || header.Version != 3 || header.ID == "11111111-1111-4111-8111-111111111111" || header.CWD != "/worktrees/slot-0002" || header.ParentSession != absoluteSource {
		t.Fatalf("header = %+v", header)
	}
	if err := client.ForkSession(context.Background(), source, target, "/worktrees/slot-0002"); err == nil {
		t.Fatal("expected existing target refusal")
	}
}

func TestSessionCWDEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, err := pi.New("pi").SessionCWD(path)
	if err != nil || cwd != "" {
		t.Fatalf("cwd=%q err=%v", cwd, err)
	}
}
