package cli

import (
	"encoding/json"
	"testing"

	"github.com/regb/workitem/internal/coordinator"
)

func TestAgentMonitorPreservesFailedEvent(t *testing.T) {
	events := coordinatorRuntimeEvents([]coordinator.DomainEvent{{Type: "agent.settled", Payload: json.RawMessage(`{"runtime_id":"claude","failed":true}`)}})
	if len(events) != 1 || !events[0].Failed {
		t.Fatalf("%+v", events)
	}
	data, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if value["failed"] != true {
		t.Fatalf("%s", data)
	}
}
