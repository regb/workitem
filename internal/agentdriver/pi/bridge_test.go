package pi

import (
	"strings"
	"testing"
)

func TestBridgeReconcilesMissedSettledEventFromIdleContext(t *testing.T) {
	source := string(bridgeSource)
	for _, fragment := range []string{
		"activityReportedBusy = true",
		"context?.isIdle()",
		`emit("agent.settled", { data: { reconciled: true }, failed: settledFailed`,
		"setInterval(reconcileIdle, 1000)",
		"clearInterval(idleProbe)",
	} {
		if !strings.Contains(source, fragment) {
			t.Fatalf("embedded bridge is missing idle reconciliation fragment %q", fragment)
		}
	}
}
