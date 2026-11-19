package daemonapi

import (
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestAWorkingAgentNeedsNoAttentionForItsLastResponse(t *testing.T) {
	busy, idle, problem := v1.AgentStatus_AGENT_STATUS_BUSY, v1.AgentStatus_AGENT_STATUS_IDLE, v1.AgentStatus_AGENT_STATUS_PROBLEM
	for _, c := range []struct {
		name    string
		o       *v1.ItemObservation
		waiting bool
		want    bool
	}{
		{"unread reply while the agent works again", &v1.ItemObservation{AgentStatus: busy, Unread: true}, false, false},
		{"unread reply on an idle agent", &v1.ItemObservation{AgentStatus: idle, Unread: true}, false, true},
		{"idle agent waiting for input", &v1.ItemObservation{AgentStatus: idle}, false, true},
		{"idle agent while capturing", &v1.ItemObservation{AgentStatus: idle, Capturing: true}, false, false},
		{"unread reply while capturing", &v1.ItemObservation{AgentStatus: idle, Unread: true, Capturing: true}, false, false},
		{"question while capturing", &v1.ItemObservation{AgentStatus: idle, Capturing: true, Asking: true}, false, true},
		{"idle agent waiting on background work", &v1.ItemObservation{AgentStatus: idle, BackgroundTasks: 2}, false, false},
		{"unread reply while background work runs", &v1.ItemObservation{AgentStatus: idle, Unread: true, BackgroundTasks: 1}, false, false},
		{"question while busy", &v1.ItemObservation{AgentStatus: busy, Asking: true}, false, true},
		{"problem", &v1.ItemObservation{AgentStatus: problem, Unread: true}, false, true},
		{"waiting item with an unread reply", &v1.ItemObservation{AgentStatus: idle, Unread: true}, true, true},
		{"waiting item, idle agent", &v1.ItemObservation{AgentStatus: idle}, true, false},
	} {
		if got := needsAttention(c.o, c.waiting); got != c.want {
			t.Errorf("%s: needsAttention=%v, want %v", c.name, got, c.want)
		}
	}
}
