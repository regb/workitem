package app

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	reviewcore "github.com/regb/workitem/internal/app/core/review"
	"github.com/regb/workitem/internal/model"
)

// ReviewCaptureQueue coalesces settlements independently of agent activity and
// controller admission. It has at most one worker and one pending rerun per item.
type ReviewCaptureQueue struct {
	Changed func()
	mu      sync.Mutex
	ctx     context.Context
	capture func(context.Context, string)
	workers map[string]bool
	active  map[string]bool
	closed  bool
	done    sync.WaitGroup
}

func NewReviewCaptureQueue(ctx context.Context, capture func(context.Context, string)) *ReviewCaptureQueue {
	return &ReviewCaptureQueue{ctx: ctx, capture: capture, workers: map[string]bool{}, active: map[string]bool{}}
}
func (q *ReviewCaptureQueue) Trigger(item string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.ctx.Err() != nil {
		return
	}
	if _, exists := q.workers[item]; exists {
		q.workers[item] = true
		return
	}
	q.workers[item] = false
	if q.Changed != nil {
		q.Changed()
	}
	q.done.Add(1)
	go func() {
		defer q.done.Done()
		for {
			q.capture(q.ctx, item)
			q.mu.Lock()
			again := q.workers[item] && !q.closed && q.ctx.Err() == nil
			if again {
				q.workers[item] = false
			} else {
				delete(q.workers, item)
			}
			q.mu.Unlock()
			if !again && q.Changed != nil {
				q.Changed()
			}
			if !again {
				return
			}
		}
	}()
}
func (q *ReviewCaptureQueue) Capturing(item string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, scheduled := q.workers[item]
	return q.active[item] || scheduled
}
func (q *ReviewCaptureQueue) observe(item string, active bool) {
	if q.Changed != nil {
		defer q.Changed()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if active {
		q.active[item] = true
	} else {
		delete(q.active, item)
	}
}
func (q *ReviewCaptureQueue) Close() { q.mu.Lock(); q.closed = true; q.mu.Unlock(); q.done.Wait() }
func (a *App) CaptureAfterSettlement(ctx context.Context, item string) {
	m, err := a.ResolveItem(ctx, ResolveOptions{Selector: item})
	if err != nil || m.Checkout.Claim == nil || m.State != model.StateActive {
		return
	}
	id, err := model.NewID()
	if err != nil {
		return
	}
	claim := m.Checkout.Claim
	captureCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, err = a.CaptureCheckpoint(captureCtx, CheckpointOptions{ResolveOptions: ResolveOptions{Selector: item}, CaptureID: id, ClaimID: claim.ID, ClaimGeneration: claim.Generation, Trigger: "settled"})
	if err == nil || ctx.Err() != nil {
		return
	}
	if gapErr := a.recordCaptureGap(ctx, item, id, err.Error()); gapErr != nil {
		fmt.Fprintf(os.Stderr, "wi: capture failed for %s: %v; recording capture gap failed: %v\n", item, err, gapErr)
	}
}
func (a *App) recordCaptureGap(ctx context.Context, item, id, reason string) error {
	m, guard, err := a.lockItemOperation(ctx, ResolveOptions{Selector: item})
	if err != nil {
		return err
	}
	defer guard.Release()
	state, err := a.ReviewRecords.LoadReview(ctx, m.ID)
	if err != nil {
		return err
	}
	if len(reason) > 2048 {
		reason = reason[:2048]
	}
	state.Gaps = append(state.Gaps, reviewcore.CaptureGap{ID: id, Reason: reason, CreatedAt: time.Now().UTC()})
	return a.ReviewRecords.StoreReview(ctx, state, state.Version, "gap-"+id, "capture_gap", map[string]string{"gap_id": id})
}
