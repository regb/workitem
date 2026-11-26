package pi

import "testing"

func TestTUIBridgeContentAndBusyGuard(t *testing.T) {
	runBridgeScript(t, "bridge_content.mjs", "tui")
}
