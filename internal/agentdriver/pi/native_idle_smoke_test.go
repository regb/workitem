package pi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/testutil"
)

// Opt-in native smoke test. Normal tests need no Pi installation, credentials,
// model requests, or live operator resources.
func TestNativePiIdleShutdown(t *testing.T) {
	if os.Getenv("GO_WI_TEST_NATIVE_PI") != "1" {
		t.Skip("set GO_WI_TEST_NATIVE_PI=1 for isolated native Pi smoke")
	}
	path, err := exec.LookPath("pi")
	if err != nil {
		t.Fatal(err)
	}
	root := testutil.ShortTempDir(t)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(root, "profile"))
	controlPath := filepath.Join(root, "control.sock")
	eventPath := filepath.Join(root, "event.sock")
	events, err := driverprotocol.ListenEvents(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	ready := make(chan struct{}, 1)
	go func() {
		for request := range events.Requests() {
			if request.Event.Type == driverprotocol.EventRuntimeReady {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
			request.Respond(nil)
		}
	}()
	bridge, err := BridgePath()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	spec := ExecSpec{RuntimeID: "smoke-owner", WorkItemID: "smoke-item", Mode: agent.ModeHeadless, CWD: root, ControlSocketPath: controlPath, EventSocketPath: eventPath}
	env := append(os.Environ(), "WI_ID=smoke-item", "WI_AGENT_RUNTIME_ID=smoke-owner", "WI_AGENT_MODE=headless", "WI_AGENT_CONTROL_SOCKET="+controlPath, "WI_AGENT_EVENT_SOCKET="+eventPath)
	promptPath := filepath.Join(root, "system-prompt.md")
	if err := os.WriteFile(promptPath, []byte("Wi system instructions.\nRead the current work item."), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--append-system-prompt", promptPath, "--mode", "rpc", "--session", filepath.Join(root, "native.jsonl"), "--no-extensions", "--extension", bridge, "--no-skills", "--no-tools", "--no-context-files", "--no-approve"}
	go func() { done <- runNativeRPC(ctx, path, args, env, spec) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("native startup: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	command := agent.ControlCommand{ProtocolVersion: agent.RuntimeProtocolVersion, ID: "smoke-stop", RuntimeID: spec.RuntimeID, WorkItemID: spec.WorkItemID, Type: agent.CommandShutdown, RequireIdle: true}
	if err := agent.SubmitControlSocket(ctx, controlPath, command); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
