package cli

import (
	"testing"
	"time"

	"github.com/regb/workitem/internal/coordinator"
)

func TestLiveAgentLoopDoesNotBecomeProblemFromHistoryAge(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)
	for _, tt := range []struct{ state, status, want string }{
		{"active", "busy", "busy"},
		{"", "busy", "problem"},
		{"active", "problem", "problem"},
		{"settled", "idle", "idle"},
		{"failed", "problem", "problem"},
	} {
		value := coordinator.AgentObservation{AgentLoopState: tt.state, Status: tt.status, Reason: "original", LastActivityAt: &old}
		status, _ := projectedAgentStatus(value, 10*time.Minute, now)
		if status != tt.want {
			t.Fatalf("value=%+v status=%s want=%s", value, status, tt.want)
		}
	}
}
