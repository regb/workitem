package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/skills"
)

func TestValidThinkingLevel(t *testing.T) {
	for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if !validThinkingLevel(level) {
			t.Fatalf("rejected %q", level)
		}
	}
	if validThinkingLevel("extreme") || validThinkingLevel("") {
		t.Fatal("accepted invalid thinking level")
	}
}

func TestDriverLaunchesConfiguredPiWithTemporarySkill(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	tmp := filepath.Join(dir, "temp skills")
	if err := os.Mkdir(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	argsPath := filepath.Join(dir, "args")
	envPath := filepath.Join(dir, "env")
	capture := filepath.Join(dir, "skill-copy")
	backend := filepath.Join(dir, "pi-test")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$ARGS_PATH"
printf '%s\n%s\n' "$PI_CODING_AGENT_DIR" "$PI_CODING_AGENT_SESSION_DIR" > "$ENV_PATH"
while [ "$#" -gt 0 ]; do
  if [ "$1" = '--skill' ]; then
    shift
    cat "$1/SKILL.md" > "$SKILL_COPY" || exit 1
  fi
  shift
done
`
	if err := os.WriteFile(backend, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "bootstrap.sock")
	server, err := driverprotocol.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	t.Setenv(driverprotocol.BootstrapSocketEnv, socket)
	t.Setenv("ARGS_PATH", argsPath)
	t.Setenv("ENV_PATH", envPath)
	t.Setenv("SKILL_COPY", capture)
	profileDir := filepath.Join(dir, "profile")
	if err := os.Mkdir(profileDir, 0700); err != nil {
		t.Fatal(err)
	}
	globalSkills := t.TempDir()
	if err := os.Symlink(globalSkills, filepath.Join(profileDir, "skills")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(globalSkills, "wi"), 0700); err != nil {
		t.Fatal(err)
	}
	userSkill := filepath.Join(globalSkills, "wi", "SKILL.md")
	if err := os.WriteFile(userSkill, []byte("user skill"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", profileDir)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", filepath.Join(dir, "wrong-sessions"))
	config, _ := json.Marshal(Config{Executable: backend, Extensions: []string{"/tools/web-search.ts"}, Model: "openai-codex/gpt-test", Thinking: "high"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan error, 1)
	go func() {
		response, err := server.ServeOnce(ctx, driverprotocol.LaunchRequest{ProtocolVersion: driverprotocol.Version, Driver: "pi", Agent: "web", Config: config, Skills: skills.Bundled(), Runtime: driverprotocol.Runtime{ID: "runtime", WorkItemID: "item", Mode: driverprotocol.ModeTUI, CWD: dir, ControlSocketPath: filepath.Join(dir, "control.sock")}, Conversation: driverprotocol.Conversation{ID: "session", Locator: filepath.Join(dir, "session.jsonl")}})
		if err == nil && !response.Capabilities.BundledSkills {
			t.Error("missing bundled skill capability")
		}
		ready <- err
	}()
	if err := Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "/tools/web-search.ts") || !strings.Contains(string(args), "--session") || !strings.Contains(string(args), "openai-codex/gpt-test") || !strings.Contains(string(args), "high") {
		t.Fatalf("args=%s", args)
	}
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	skillPath := ""
	for i, line := range lines {
		if line == "--skill" && i+1 < len(lines) {
			skillPath = lines[i+1]
		}
	}
	if !filepath.IsAbs(skillPath) {
		t.Fatalf("skill path=%q args=%s", skillPath, args)
	}
	if _, err := os.Stat(filepath.Dir(skillPath)); !os.IsNotExist(err) {
		t.Fatalf("temporary skill survived backend exit: %v", err)
	}
	b, err := os.ReadFile(capture)
	if err != nil || string(b) != string(skills.Bundled()[0].Files[0].Data) {
		t.Fatalf("backend did not receive bundled skill: %v", err)
	}
	b, err = os.ReadFile(userSkill)
	if err != nil || string(b) != "user skill" {
		t.Fatalf("user skill changed: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(profileDir, "skills")); err != nil || target != globalSkills {
		t.Fatalf("global skill symlink changed: %v", err)
	}
	environment, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(environment), profileDir+"\n"+dir+"\n"; got != want {
		t.Fatalf("backend directories=%q want=%q", got, want)
	}
}
