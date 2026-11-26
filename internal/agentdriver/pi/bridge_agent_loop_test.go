package pi

import "testing"

func TestTUIBridgeKeepsRetryActiveAndReportsSettlementOutcome(t *testing.T) {
	runBridgeScript(t, "bridge_agent_loop.mjs", "tui")
}
