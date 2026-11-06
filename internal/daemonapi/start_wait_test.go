package daemonapi

import (
	"context"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app"
	viewapp "github.com/regb/workitem/internal/app/view"
)

// A new agent is unobserved, then busy, while it starts; its first prompt
// waits for it to be idle instead of being refused as busy.
func TestStartWaitsForTheNewAgentToBeIdle(t *testing.T) {
	calls := 0
	s := &Server{App: &app.App{WorkListObserver: func(context.Context, string) (viewapp.Observation, error) {
		calls++
		switch {
		case calls == 1:
			return viewapp.Observation{}, nil
		case calls == 2:
			return viewapp.Observation{Status: "busy"}, nil
		}
		return viewapp.Observation{Status: "idle"}, nil
	}}}
	start := time.Now()
	s.waitAgentIdle(context.Background(), "item", 10*time.Second)
	if calls != 3 || time.Since(start) > 5*time.Second {
		t.Fatalf("waited %v over %d observations", time.Since(start), calls)
	}
	// An agent that never settles waits only up to the timeout.
	s.App.WorkListObserver = func(context.Context, string) (viewapp.Observation, error) {
		return viewapp.Observation{Status: "busy"}, nil
	}
	start = time.Now()
	s.waitAgentIdle(context.Background(), "item", 600*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("timeout wait took %v", elapsed)
	}
}
