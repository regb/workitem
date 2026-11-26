package daemonapi

import (
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestSetupBlocksAgentOnlyWhileRunningOrFailed(t *testing.T) {
	for _, test := range []struct {
		setup *v1.SetupRun
		want  bool
	}{
		{nil, false},
		{&v1.SetupRun{State: v1.SetupState_SETUP_STATE_SUCCEEDED}, false},
		{&v1.SetupRun{State: v1.SetupState_SETUP_STATE_RUNNING}, true},
		{&v1.SetupRun{State: v1.SetupState_SETUP_STATE_FAILED}, true},
	} {
		if got := setupBlocksAgent(test.setup); got != test.want {
			t.Errorf("setupBlocksAgent(%v) = %v, want %v", test.setup, got, test.want)
		}
	}
}
