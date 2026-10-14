package claude

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

func TestConversationLaunchArgsPreserveNativeContent(t *testing.T) {
	for _, state := range []string{"absent", "empty", "nonempty", "directory", "symlink"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "transcript.jsonl")
			switch state {
			case "empty":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "nonempty":
				if err := os.WriteFile(path, []byte("native content\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(dir, "target")
				if err := os.WriteFile(target, []byte("native content\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			args, err := conversationLaunchArgs(path, "session")
			if state == "directory" || state == "symlink" {
				if err == nil {
					t.Fatal("nonregular transcript accepted")
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("nonregular transcript changed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			flag := "--session-id"
			if state == "nonempty" {
				flag = "--resume"
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "native content\n" {
					t.Fatal("native transcript changed", err)
				}
			} else if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("placeholder remains")
			}
			if !reflect.DeepEqual(args, []string{flag, "session"}) {
				t.Fatal(args)
			}
		})
	}
}

func TestSharedRuntimeSetupBothModes(t *testing.T) {
	for _, mode := range []protocol.Mode{protocol.ModeTUI, protocol.ModeHeadless} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			locator := filepath.Join(root, "sessions", uuid("shared")+".jsonl")
			control := filepath.Join(root, "control.sock")
			request := protocol.LaunchRequest{Runtime: protocol.Runtime{Mode: mode, ID: "runtime", WorkItemID: "item", CWD: root, ControlSocketPath: control}, Conversation: protocol.Conversation{Locator: locator}, Environment: map[string]string{"HOME": root, "CLAUDE_CONFIG_DIR": filepath.Join(root, ".claude"), "TMUX": "private socket", "TMUX_PANE": "%1", "TMUX_TMPDIR": root, "WI_ID": "item"}, Skills: []protocol.Skill{{Name: "wi", Files: []protocol.SkillFile{{Path: "SKILL.md", Data: []byte("fixture skill")}}}}}
			p, err := prepareRuntime(request, Config{Executable: "sh", Model: "haiku", Effort: "low"})
			if err != nil {
				t.Fatal(err)
			}
			defer p.close()
			env := strings.Join(p.env, "\n")
			for _, key := range []string{"TMUX=", "TMUX_PANE=", "TMUX_TMPDIR="} {
				if strings.Contains(env, key) != (mode == protocol.ModeTUI) {
					t.Fatal("wrong mode environment", key)
				}
			}
			if !strings.Contains(env, "WI_ID=item") {
				t.Fatal("runtime identity lost")
			}
			args, err := p.args("--mode-fixture")
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"--dangerously-skip-permissions", "--append-system-prompt", workItemSystemPrompt, "--mode-fixture", "--session-id", p.session, "--plugin-dir", p.skills, "--model", "haiku", "--effort", "low"}
			if !reflect.DeepEqual(args, want) {
				t.Fatal(args)
			}
			skillPath := filepath.Join(p.skills, "skills/wi/SKILL.md")
			if data, err := os.ReadFile(skillPath); err != nil || string(data) != "fixture skill" {
				t.Fatal(err)
			}
			p.close()
			for _, path := range []string{control, protocol.ContentSocketPath(control), p.skills} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("resource not cleaned", path)
				}
			}
		})
	}
}

func TestSharedRuntimeSetupFailureClosesEndpoints(t *testing.T) {
	root := t.TempDir()
	control := filepath.Join(root, "control.sock")
	r := protocol.LaunchRequest{Runtime: protocol.Runtime{Mode: protocol.ModeHeadless, ID: "runtime", WorkItemID: "item", CWD: root, ControlSocketPath: control}, Conversation: protocol.Conversation{Locator: filepath.Join(root, uuid("bad-skill")+".jsonl")}, Environment: map[string]string{"HOME": root, "CLAUDE_CONFIG_DIR": filepath.Join(root, ".claude")}, Skills: []protocol.Skill{{Name: "../invalid"}}}
	if _, err := prepareRuntime(r, Config{Executable: "sh"}); err == nil {
		t.Fatal("bad skill accepted")
	}
	for _, path := range []string{control, protocol.ContentSocketPath(control)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("endpoint retained after failure", path)
		}
	}
}
