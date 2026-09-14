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
)

func TestDriverLaunchesConfiguredPiExecutableAndExtension(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	argsPath := filepath.Join(dir, "args")
	envPath := filepath.Join(dir, "env")
	backend := filepath.Join(dir, "pi-test")
	if err := os.WriteFile(backend, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_PATH\"\nprintf '%s\\n%s\\n' \"$PI_CODING_AGENT_DIR\" \"$PI_CODING_AGENT_SESSION_DIR\" > \"$ENV_PATH\"\n"), 0o700); err != nil {
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
	profileDir := filepath.Join(dir, "profile")
	t.Setenv("PI_CODING_AGENT_DIR", profileDir)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", filepath.Join(dir, "wrong-sessions"))
	config, _ := json.Marshal(Config{Executable: backend, Extensions: []string{"/tools/web-search.ts"}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan error, 1)
	go func() {
		_, err := server.ServeOnce(ctx, driverprotocol.LaunchRequest{ProtocolVersion: driverprotocol.Version, Driver: "pi", Agent: "web", Config: config, Runtime: driverprotocol.Runtime{ID: "runtime", WorkItemID: "item", Mode: driverprotocol.ModeTUI, CWD: dir, ControlSocketPath: filepath.Join(dir, "control.sock")}, Conversation: driverprotocol.Conversation{ID: "session", Locator: filepath.Join(dir, "session.jsonl")}})
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
	got := string(args)
	if !strings.Contains(got, "/tools/web-search.ts") || !strings.Contains(got, "--session") {
		t.Fatalf("backend args:\n%s", got)
	}
	environment, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(environment), profileDir+"\n"+dir+"\n"; got != want {
		t.Fatalf("backend profile and session directories = %q, want %q", got, want)
	}
}
