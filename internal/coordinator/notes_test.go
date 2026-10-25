package coordinator

import "testing"

func TestAgentsMayKeepNotesButNotEditItems(t *testing.T) {
	for _, method := range []string{MethodAddNote, MethodAttach} {
		if !agentMethodAllowed(method) {
			t.Errorf("agent endpoint refuses %s", method)
		}
	}
	if agentMethodAllowed(MethodEditItem) {
		t.Error("agent endpoint allows item edits")
	}
}
