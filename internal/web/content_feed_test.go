package web

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

// streamingBackend streams the events sent on its channel, once per subscription.
type streamingBackend struct {
	fakeBackend
	subscriptions atomic.Int32
	events        chan driverprotocol.ContentEvent
	stopped       chan struct{}
}

func (b *streamingBackend) ContentStream(ctx context.Context, _ string, apply func(driverprotocol.ContentEvent) error) error {
	b.subscriptions.Add(1)
	defer close(b.stopped)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-b.events:
			if err := apply(event); err != nil {
				return err
			}
		}
	}
}

// waitNext waits until the feed has something new for the cursor.
func waitNext(t *testing.T, feed *contentFeed, cursor feedCursor) (*driverprotocol.Content, []driverprotocol.ContentEvent, feedCursor) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		snapshot, events, _, after, changed := feed.next(cursor)
		if snapshot != nil || len(events) > 0 {
			return snapshot, events, after
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("feed never changed")
		}
	}
}

func TestContentFeedSharesOneSubscriptionAndSendsEvents(t *testing.T) {
	backend := &streamingBackend{events: make(chan driverprotocol.ContentEvent), stopped: make(chan struct{})}
	feeds := newContentFeeds(backend)
	first, leaveFirst := feeds.join("item")
	second, leaveSecond := feeds.join("item")
	if first != second {
		t.Fatal("tabs on one item got separate feeds")
	}
	backend.events <- driverprotocol.ContentEvent{Sequence: 5, Type: driverprotocol.ContentSnapshot, Snapshot: &driverprotocol.Content{Sequence: 5, Text: "Hel"}}
	snapshot, _, caughtUp := waitNext(t, first, feedCursor{})
	if snapshot == nil || snapshot.Text != "Hel" {
		t.Fatalf("a new tab starts from a snapshot, got %+v", snapshot)
	}
	backend.events <- driverprotocol.ContentEvent{Sequence: 6, Type: driverprotocol.ContentMessageDelta, Text: "lo"}
	snapshot, events, _ := waitNext(t, first, caughtUp)
	if snapshot != nil || len(events) != 1 || events[0].Text != "lo" {
		t.Fatalf("a caught-up tab gets only new events: %+v %+v", snapshot, events)
	}
	// A tab that never read starts from the current snapshot, events included.
	if snapshot, _, _ := waitNext(t, second, feedCursor{}); snapshot == nil || snapshot.Text != "Hello" {
		t.Fatalf("late tab snapshot = %+v", snapshot)
	}
	if n := backend.subscriptions.Load(); n != 1 {
		t.Fatalf("subscriptions = %d", n)
	}
	leaveFirst()
	select {
	case <-backend.stopped:
		t.Fatal("subscription stopped while a tab still follows it")
	case <-time.After(50 * time.Millisecond):
	}
	leaveSecond()
	select {
	case <-backend.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("subscription outlived its last tab")
	}
}

// pollingBackend runs a driver that only answers snapshot reads.
type pollingBackend struct {
	fakeBackend
	text atomic.Value
}

func (b *pollingBackend) ContentStream(context.Context, string, func(driverprotocol.ContentEvent) error) error {
	return driverprotocol.ErrContentStreamUnsupported
}
func (b *pollingBackend) Content(context.Context, string) (driverprotocol.Content, error) {
	return driverprotocol.Content{Text: b.text.Load().(string)}, nil
}

func TestContentFeedPollsSnapshotOnlyRuntimes(t *testing.T) {
	backend := &pollingBackend{}
	backend.text.Store("one")
	feed, leave := newContentFeeds(backend).join("item")
	defer leave()
	snapshot, _, cursor := waitNext(t, feed, feedCursor{})
	if snapshot == nil || snapshot.Text != "one" {
		t.Fatalf("first poll = %+v", snapshot)
	}
	backend.text.Store("two")
	if snapshot, _, _ := waitNext(t, feed, cursor); snapshot == nil || snapshot.Text != "two" {
		t.Fatalf("changed poll = %+v", snapshot)
	}
}

func TestMessageClientIDsAreBoundedTokens(t *testing.T) {
	for id, valid := range map[string]bool{"": true, "1792429-ab3f": true, "a.b_c-1": true, "has space": false, "web:x": false, string(make([]byte, 65)): false} {
		message := Message{Text: "hi", Delivery: "prompt", ClientID: id}
		if err := validateMessage(&message); (err == nil) != valid {
			t.Fatalf("client id %q: err=%v", id, err)
		}
	}
}

// changesBackend reports the changes sent on its channel and counts list reads.
type changesBackend struct {
	fakeBackend
	changes  chan Changes
	reads    atomic.Int32
	inspects atomic.Int32
}

func (b *changesBackend) Changes(ctx context.Context, epoch string, _ uint64, _ time.Duration) (Changes, error) {
	if epoch == "" {
		return Changes{Epoch: "e", Revision: 1, All: true}, nil
	}
	select {
	case <-ctx.Done():
		return Changes{}, ctx.Err()
	case changes := <-b.changes:
		return changes, nil
	}
}
func (b *changesBackend) List(_ context.Context, inspect bool) (List, error) {
	if inspect {
		b.inspects.Add(1)
	}
	b.reads.Add(1)
	return List{}, nil
}

func TestListHubFollowsChangesPerItem(t *testing.T) {
	backend := &changesBackend{changes: make(chan Changes)}
	hub := newListHub(backend)
	leave := hub.subscribe()
	defer leave()
	settled := func(want int32) {
		t.Helper()
		for end := time.Now().Add(3 * time.Second); backend.reads.Load() != want; {
			if time.Now().After(end) {
				t.Fatalf("list read %d times, want %d", backend.reads.Load(), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	settled(1)
	time.Sleep(20 * time.Millisecond)
	_, a, _ := hub.current("a")
	_, b, wake := hub.current("b")
	backend.changes <- Changes{Epoch: "e", Revision: 2, Items: []string{"b"}}
	<-wake
	settled(2)
	time.Sleep(20 * time.Millisecond)
	if _, now, _ := hub.current("a"); now != a {
		t.Fatal("an unchanged item's conversation would be re-read")
	}
	if _, now, _ := hub.current("b"); now == b {
		t.Fatal("a changed item's conversation would not be re-read")
	}
	// Without events, the list is not read again.
	time.Sleep(100 * time.Millisecond)
	settled(2)
	// Only the first read inspects; an event's change was already observed.
	if got := backend.inspects.Load(); got != 1 {
		t.Fatalf("inspected %d times", got)
	}
	backend.changes <- Changes{Epoch: "e", Revision: 3}
	settled(3)
	if got := backend.inspects.Load(); got != 2 {
		t.Fatalf("the fallback beat did not inspect: %d", got)
	}
}
