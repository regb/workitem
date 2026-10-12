package app

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/regb/workitem/internal/lock"
)

// settledExecutorSync coalesces settled-turn exports without blocking event ACKs.
// Failures retain the previous export and are diagnostic, not lifecycle changes.
func (a *App) settledExecutorSync(parent context.Context, item, runtime, claim string) (chan<- struct{}, func()) {
	ctx, cancel := context.WithCancel(parent)
	wake := make(chan struct{}, 1)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
			}
			syncCtx, stop := context.WithTimeout(ctx, 30*time.Second)
			err := a.syncExecutor(syncCtx, item, runtime, claim)
			stop()
			if err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "wi: executor export deferred: %v\n", err)
			}
		}
	}()
	return wake, func() { cancel(); workers.Wait() }
}
func (a *App) syncExecutor(ctx context.Context, item, runtime, claim string) error {
	guard, err := lock.Acquire(ctx, a.Store.LockPath(item)+".executor")
	if err != nil {
		return err
	}
	defer guard.Release()
	m, err := a.Store.LoadManifest(item)
	if err != nil {
		return err
	}
	owner, err := a.Store.LoadAgentRuntime(item)
	if err != nil {
		return err
	}
	if owner == nil || owner.ID != runtime || m.Checkout.Claim == nil || m.Checkout.Claim.ID != claim {
		return nil
	}
	b := m.Checkout.Claim.Executor
	if b == nil || !b.Ready || b.Releasing {
		return nil
	}
	c, err := a.executorClient(m)
	if err != nil {
		return err
	}
	status, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if status.Branch != m.Checkout.Specification.Branch {
		return fmt.Errorf("executor branch differs from item branch")
	}
	if status.Head == b.ExportedHead {
		return nil
	}
	_, err = a.exportExecutor(ctx, m)
	return err
}
