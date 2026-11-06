package app

import (
	"context"
	"fmt"
	"time"
)

// WaitForAgentStopped fences a later claim release or start against the
// process identity of the runtime that accepted shutdown, not its PID alone.
func (a *App) WaitForAgentStopped(ctx context.Context, itemID string) error {
	runtime, err := a.Store.LoadAgentRuntime(itemID)
	if err != nil {
		return err
	}
	timeout := a.ArchiveRuntimeStopTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("agent shutdown was accepted but its process has not exited")
		case <-ticker.C:
		}
	}
	return nil
}
