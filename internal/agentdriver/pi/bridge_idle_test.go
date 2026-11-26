package pi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHeadlessBridgeChecksIdleInsidePiWithoutOpeningSockets(t *testing.T) {
	root := runBridgeScript(t, "bridge_idle.mjs", "headless")
	if _, err := os.Stat(filepath.Join(root, "control.sock")); !os.IsNotExist(err) {
		t.Fatal("headless bridge opened control socket")
	}
}
