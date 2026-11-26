package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/testutil"
)

func TestNativeRPCRequiresEventSocket(t *testing.T) {
	dir := t.TempDir()
	fakePi := filepath.Join(dir, "fake-pi")
	testutil.WriteScript(t, fakePi, "exit 0")
	err := runNativeRPC(context.Background(), fakePi, nil, os.Environ(), ExecSpec{
		CWD: dir, WorkItemID: "01K00000000000000000000000", RuntimeID: "headless-socket", ControlSocketPath: filepath.Join(dir, "control.sock"),
	})
	if err == nil || !strings.Contains(err.Error(), "requires an event socket path") {
		t.Fatalf("err = %v", err)
	}
}
