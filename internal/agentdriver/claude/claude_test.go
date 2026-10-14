package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/testutil"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 5 && os.Args[1] == "internal-driver" && os.Args[2] == "claude" && os.Args[3] == "hook" {
		if err := RunHook(context.Background(), os.Args[4], os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv("WI_CLAUDE_TEST_BACKEND") == "1" {
		os.Exit(m.Run())
	}
	os.Exit(testutil.RunIsolatedTests(m.Run))
}
func TestConfiguration(t *testing.T) {
	for _, raw := range []string{`{"extensions":[]}`, `{"args":["--session-id","other"]}`, `{"effort":"bad"}`, `{"permission_mode":"bypassPermissions"}`, `{"permission_mode":"plan"}`, `{"permission_mode":"default"}`, `{"model":"--version"}`, `{} {}`} {
		if _, err := parseConfig([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	c, err := parseConfig([]byte(`{"model":"sonnet","effort":"high"}`))
	if err != nil || c.Executable != "claude" {
		t.Fatalf("%+v %v", c, err)
	}
}
func TestConversationLifecycle(t *testing.T) {
	response, err := conversationOperation(protocol.Request{Operation: protocol.OperationConversationCreate, Conversation: &protocol.ConversationRequest{ID: "01M4RGTJXPRJCEVN3TZVE18W5X"}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Join(root, response.Conversation.Locator)
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"user","uuid":"u1","sessionId":"old","cwd":"/old","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"TASK_SECRET"}}` + "\n" + `{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"old","cwd":"/old","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ANSWER_SECRET"}]}}` + "\n"
	if err := os.WriteFile(source, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "fork", uuid("new")+".jsonl")
	if err := fork(source, target, "/new"); err != nil {
		t.Fatal(err)
	}
	got, err := conversationOperation(protocol.Request{Operation: protocol.OperationConversationCWD, Conversation: &protocol.ConversationRequest{Path: target}})
	if err != nil || got.CWD != "/new" {
		t.Fatalf("cwd=%+v %v", got, err)
	}
	original, _ := os.ReadFile(source)
	if string(original) != data {
		t.Fatal("fork edited source")
	}
	facts, err := conversationOperation(protocol.Request{Operation: protocol.OperationConversationDecode, Conversation: &protocol.ConversationRequest{StartLine: 5, Lines: [][]byte{[]byte(strings.Split(data, "\n")[0]), []byte(strings.Split(data, "\n")[1]), []byte(`malformed`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(facts.Decoded, []bool{true, true, false}) || facts.Facts[0].Line != 5 || !facts.Facts[1].Terminal {
		t.Fatalf("facts=%+v", facts)
	}
	encoded, _ := json.Marshal(facts)
	if strings.Contains(string(encoded), "SECRET") {
		t.Fatal("native text leaked into compact facts")
	}
	observed, warnings := observe(source, time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC))
	if len(warnings) != 0 || observed.InferredTurnState != "idle" || observed.LastActivityAgeSeconds != 1 {
		t.Fatalf("%+v %v", observed, warnings)
	}
	if err := fork(source, target, "/new"); err == nil {
		t.Fatal("fork overwrote destination")
	}
}
func TestStorageOwnership(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(t.TempDir(), uuid("test")+".jsonl")
	env, native, err := prepareStorage(path, uuid("test"), map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": "", "CLAUDECODE": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(native, filepath.Base(path)) {
		t.Fatal(native)
	}
	for _, value := range env {
		if strings.HasPrefix(value, "CLAUDECODE=") {
			t.Fatal("nested session marker leaked")
		}
	}
	if _, _, err := prepareStorage(path, uuid("test"), map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": ""}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareStorage(filepath.Join(t.TempDir(), filepath.Base(path)), uuid("test"), map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": ""}); err == nil {
		t.Fatal("namespace reassigned another conversation directory")
	}
}
func TestHeadlessMissingExecutable(t *testing.T) {
	var ready protocol.Ready
	err := launch(context.Background(), protocol.LaunchRequest{Runtime: protocol.Runtime{Mode: protocol.ModeHeadless}}, Config{Executable: "not-installed"}, func(r protocol.Ready) error { ready = r; return nil })
	if err == nil || !strings.Contains(ready.Error, "not-installed") {
		t.Fatalf("%+v %v", ready, err)
	}
}
func TestBundledSkills(t *testing.T) {
	dir, cleanup, err := exportSkills([]protocol.Skill{{Name: "wi", Files: []protocol.SkillFile{{Path: "SKILL.md", Data: []byte("skill body")}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, err := os.ReadFile(filepath.Join(dir, "skills", "wi", "SKILL.md"))
	if err != nil || string(got) != "skill body" {
		t.Fatalf("%q %v", got, err)
	}
	if _, _, err := exportSkills([]protocol.Skill{{Name: "wi", Files: []protocol.SkillFile{{Path: "../escape", Data: []byte("bad")}}}}); err == nil {
		t.Fatal("unsafe skill accepted")
	}
}
