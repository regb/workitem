package daemonapi

import (
	"context"
	"sync"
	"testing"
)

func TestRegistryShutdownRacesWithNewJobs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	registry := newJobRegistry(ctx)
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			registry.start("race", "", "", func(ctx context.Context, _ *jobRun) error { <-ctx.Done(); return ctx.Err() })
		}()
	}
	cancel()
	registry.wait()
	group.Wait()
}
