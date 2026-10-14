package claude

import (
	"time"
)

// Native prompt identities fence late lifecycle hooks. Hook-start time is a
// conservative fallback when a backend omits the identity; it is not atomic
// idle control and cannot establish execution merely from prompt receipt.
type turnTracker struct {
	promptID             string
	commandID, requestID string
	started              time.Time
	executing, settled   bool
	retired              map[string]bool
	retiredOrder         []string
}

func (t *turnTracker) accept(h hookInput) bool {
	if h.Event == "SessionStart" || h.Event == "SessionEnd" {
		return h.ObservedAt.IsZero() || t.started.IsZero() || !h.ObservedAt.Before(t.started)
	}
	if h.Event == "UserPromptSubmit" {
		if h.PromptID != "" && t.retired[h.PromptID] {
			return false
		}
		if !h.ObservedAt.IsZero() && h.ObservedAt.Before(t.started) {
			return false
		}
		if h.PromptID != "" && h.PromptID == t.promptID {
			return false
		}
		if t.promptID != "" {
			if t.retired == nil {
				t.retired = map[string]bool{}
			}
			t.retired[t.promptID] = true
			t.retiredOrder = append(t.retiredOrder, t.promptID)
			if len(t.retiredOrder) > 1024 {
				delete(t.retired, t.retiredOrder[0])
				t.retiredOrder = t.retiredOrder[1:]
			}
		}
		t.commandID = ""
		t.requestID = ""
		t.promptID = h.PromptID
		t.started = h.ObservedAt
		if t.started.IsZero() {
			t.started = time.Now()
		}
		t.executing = false
		t.settled = false
		return true
	}
	if h.PromptID != "" && t.promptID != "" && h.PromptID != t.promptID {
		return false
	}
	if !h.ObservedAt.IsZero() && !t.started.IsZero() && h.ObservedAt.Before(t.started) {
		return false
	}
	if t.settled && (h.Event == "Stop" || h.Event == "StopFailure") {
		return false
	}
	return true
}
func (t *turnTracker) matches(promptID string, at time.Time) bool {
	if promptID != "" && t.promptID != "" && promptID != t.promptID {
		return false
	}
	return at.IsZero() || t.started.IsZero() || !at.Before(t.started)
}
