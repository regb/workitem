package app

import (
	"context"
	"testing"
	"time"
)

func TestReviewCaptureQueueCoalescesSettlementsWithoutHoldingTrigger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 4)
	release := make(chan struct{})
	q := NewReviewCaptureQueue(ctx, func(ctx context.Context, item string) {
		started <- item
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	defer q.Close()
	q.Trigger("item")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("capture did not start")
	}
	for i := 0; i < 100; i++ {
		q.Trigger("item")
	}
	// Other items run independently; all of these triggers return while Git is
	// still blocked. Neither scheduling nor observation is agent busy state.
	q.Trigger("other")
	select {
	case item := <-started:
		if item != "other" {
			t.Fatal(item)
		}
	case <-time.After(time.Second):
		t.Fatal("capture of one item blocked another")
	}
	q.observe("item", true)
	if !q.Capturing("item") {
		t.Fatal("missing capture observation")
	}
	q.observe("item", false)
	close(release)
	select {
	case item := <-started:
		if item != "item" {
			t.Fatal(item)
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced follow-up missing")
	}
	q.Close()
	select {
	case item := <-started:
		t.Fatal("more than one coalesced follow-up", item)
	default:
	}
}
