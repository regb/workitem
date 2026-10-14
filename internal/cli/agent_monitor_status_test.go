package cli

import (
	"bytes"
	"encoding/json"
	"github.com/regb/workitem/internal/coordinator"
	"strings"
	"testing"
	"time"
)

func TestMonitorKeepsSafeDeliveryAndReadinessFacts(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"runtime_id": "runtime", "command_id": "cmd", "request_id": "request", "failed": true, "readiness": "blocked", "reason_code": "paste_readback_missing", "delivery_state": "uncertain"})
	events := coordinatorRuntimeEvents([]coordinator.DomainEvent{{ID: "event", ItemID: "item", Type: "command.rejected", Timestamp: time.Now(), Payload: payload}})
	e := events[0]
	if e.Readiness != "blocked" || e.ReasonCode != "paste_readback_missing" || e.DeliveryState != "uncertain" || e.CommandID != "cmd" || e.RequestID != "request" {
		t.Fatal(e)
	}
	var output bytes.Buffer
	if err := printAgentRuntimeEvent(Config{Stdout: &output}, e, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"readiness=blocked", "delivery=uncertain", "submission shortcut not sent", "do not resend"} {
		if !strings.Contains(output.String(), want) {
			t.Fatal(output.String())
		}
	}
}
