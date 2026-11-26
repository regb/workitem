package agentdriver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/skills"

	"github.com/regb/workitem/internal/testutil"
)

func TestRealBinaryPassesTemporaryBundledSkillThroughDriver(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the real binary")
	}
	root := testutil.ShortTempDir(t)
	binary := filepath.Join(root, "wi")
	build := exec.Command("go", "build", "-o", binary, "./cmd/wi")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	profile := filepath.Join(root, "profile")
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	globalSkills := filepath.Join(root, "global-skills")
	if err := os.MkdirAll(filepath.Join(globalSkills, "wi"), 0700); err != nil {
		t.Fatal(err)
	}
	userSkill := filepath.Join(globalSkills, "wi", "SKILL.md")
	if err := os.WriteFile(userSkill, []byte("custom Wi skill"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(globalSkills, filepath.Join(profile, "skills")); err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(root, "fake-pi")
	capture := filepath.Join(root, "captured-skill")
	argsPath := filepath.Join(root, "args")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$ARGS_PATH"
while [ "$#" -gt 0 ]; do
  if [ "$1" = '--skill' ]; then
    shift
    cp -R "$1" "$SKILL_COPY"
  fi
  shift
done
`
	if err := os.WriteFile(backend, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	env := append([]string(nil), os.Environ()...)
	env = append(env, "HOME="+root, "PI_CODING_AGENT_DIR="+profile, "XDG_CACHE_HOME="+filepath.Join(root, "cache"), "ARGS_PATH="+argsPath, "SKILL_COPY="+capture)
	config, _ := json.Marshal(map[string]string{"executable": backend})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resources := skills.Bundled()
	request := protocol.LaunchRequest{ProtocolVersion: protocol.Version, Driver: "pi", Agent: "default", Config: config, Skills: resources, Runtime: protocol.Runtime{ID: "runtime", WorkItemID: "item", Mode: protocol.ModeTUI, CWD: root, ControlSocketPath: filepath.Join(root, "control.sock")}, Conversation: protocol.Conversation{ID: "conversation", Locator: filepath.Join(root, "conversation.jsonl"), Format: "pi-jsonl"}}
	if err := Run(ctx, binary, []string{"internal-driver", "pi"}, request, env, func(ready protocol.Ready) error {
		if !ready.Capabilities.BundledSkills {
			t.Error("driver omitted bundled skill capability")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, file := range resources[0].Files {
		b, err := os.ReadFile(filepath.Join(capture, filepath.FromSlash(file.Path)))
		if err != nil || string(b) != string(file.Data) {
			t.Fatalf("bundled file %s mismatch: %v", file.Path, err)
		}
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	skillPath := ""
	for i, arg := range lines {
		if arg == "--skill" && i+1 < len(lines) {
			skillPath = lines[i+1]
		}
	}
	if !filepath.IsAbs(skillPath) {
		t.Fatalf("skill path=%q args=%s", skillPath, args)
	}
	if _, err := os.Stat(filepath.Dir(skillPath)); !os.IsNotExist(err) {
		t.Fatalf("temporary skill survived runtime exit: %v", err)
	}
	b, err := os.ReadFile(userSkill)
	if err != nil || string(b) != "custom Wi skill" {
		t.Fatalf("global skill changed: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(profile, "skills")); err != nil || target != globalSkills {
		t.Fatalf("global skill root changed: %v", err)
	}
}
