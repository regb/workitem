package pi

import "testing"

func TestTUIBridgeRecoversFromRelayRejectionAndRetainsOwnerFence(t *testing.T) {
	runBridgeScript(t, "bridge_delivery.mjs", "tui")
}
