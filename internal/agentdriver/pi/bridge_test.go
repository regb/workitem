package pi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/testutil"
)

// The bridge tests run the embedded TypeScript bridge under Node with a fake
// Pi extension context; they need Node's type stripping, not Pi, tmux,
// credentials or a model.

// bridgeCommand copies the embedded bridge and testdata/name into a fresh short
// directory and returns a Node command running the script as runtime "owner"
// of item "item" in mode, with its control and event sockets in that directory.
// WI_CONTENT_TEST_SOCKET names the content socket the bridge derives from its
// control socket.
func bridgeCommand(t *testing.T, ctx context.Context, name, mode string, env ...string) (*exec.Cmd, string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("skipping bridge test: node is not on PATH")
	}
	if err := exec.Command(node, "--experimental-strip-types", "--eval", "").Run(); err != nil {
		t.Skipf("skipping bridge test: %s does not support --experimental-strip-types: %v", node, err)
	}
	script, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	root := testutil.ShortTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "bridge.ts"), bridgeSource, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "test.mjs"), script, 0600); err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(root, "control.sock")
	cmd := exec.CommandContext(ctx, node, "--experimental-strip-types", filepath.Join(root, "test.mjs"))
	cmd.Env = append(os.Environ(), "WI_ID=item", "WI_AGENT_RUNTIME_ID=owner", "WI_AGENT_MODE="+mode,
		"WI_AGENT_CONTROL_SOCKET="+control, "WI_AGENT_EVENT_SOCKET="+filepath.Join(root, "event.sock"),
		"WI_CONTENT_TEST_SOCKET="+driverprotocol.ContentSocketPath(control))
	cmd.Env = append(cmd.Env, env...)
	return cmd, root
}

// runBridgeScript runs testdata/name to completion (see bridgeCommand) and
// fails with its output if it exits unsuccessfully. It returns the directory
// holding the sockets.
func runBridgeScript(t *testing.T, name, mode string, env ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd, root := bridgeCommand(t, ctx, name, mode, env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s (%s): %v\n%s", name, mode, err, output)
	}
	return root
}

func TestBridgeObservesSelectedBranchAndNativeLifecycle(t *testing.T) {
	for _, mode := range []string{"tui", "headless"} {
		t.Run(mode, func(t *testing.T) {
			runBridgeScript(t, "bridge_observation.mjs", mode)
		})
	}
}
