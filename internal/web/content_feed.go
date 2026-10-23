package web

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

// Content feed timing. A runtime that only answers snapshot reads is polled;
// with no runtime, the feed retries until one appears.
const (
	contentPollInterval  = 400 * time.Millisecond
	contentRetryInterval = time.Second
	contentResumeDelay   = 100 * time.Millisecond
	// maxFeedEvents and maxFeedBytes bound the events kept since the last
	// snapshot; tool updates carry whole outputs, so bytes matter. A tab
	// further behind receives a fresh snapshot instead.
	maxFeedEvents = 2048
	maxFeedBytes  = 4 << 20
)

// contentFeeds shares one driver content subscription per item among all
// open tabs, and stops it when the last tab leaves.
type contentFeeds struct {
	backend Backend
	mu      sync.Mutex
	feeds   map[string]*contentFeed
}

func newContentFeeds(backend Backend) *contentFeeds {
	return &contentFeeds{backend: backend, feeds: map[string]*contentFeed{}}
}

// join returns the item's feed and a function that leaves it.
func (f *contentFeeds) join(id string) (*contentFeed, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	feed := f.feeds[id]
	if feed == nil {
		ctx, cancel := context.WithCancel(context.Background())
		feed = &contentFeed{cancel: cancel, changed: make(chan struct{})}
		f.feeds[id] = feed
		go feed.run(ctx, f.backend, id)
	}
	feed.users++
	return feed, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		feed.users--
		if feed.users == 0 {
			feed.cancel()
			delete(f.feeds, id)
		}
	}
}

// contentFeed is one item's live content: the current snapshot, and the
// events applied since that snapshot was last replaced. Epoch changes on
// every replacement, so a tab knows whether it can continue with events.
type contentFeed struct {
	cancel  context.CancelFunc
	users   int
	mu      sync.Mutex
	epoch   uint64
	value   driverprotocol.Content
	events  []driverprotocol.ContentEvent
	bytes   int
	changed chan struct{}
}

func (f *contentFeed) run(ctx context.Context, backend Backend, id string) {
	for ctx.Err() == nil {
		received := false
		err := backend.ContentStream(ctx, id, func(event driverprotocol.ContentEvent) error {
			received = true
			f.apply(event)
			return nil
		})
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, driverprotocol.ErrContentStreamUnsupported):
			f.poll(ctx, backend, id)
		case received:
			// A stream that ran and ended (a gap, a dropped reader) resumes
			// from a fresh snapshot; what tabs show stays meanwhile.
			wait(ctx, contentResumeDelay)
		default:
			// No runtime, or it stopped: show why until a runtime appears.
			f.replace(driverprotocol.Content{Error: err.Error()})
			wait(ctx, contentRetryInterval)
		}
	}
}

// poll reads snapshots from a runtime that cannot stream, until it stops.
// A failed read shows why and waits before run tries again, so a runtime
// that refuses both never spins.
func (f *contentFeed) poll(ctx context.Context, backend Backend, id string) {
	for ctx.Err() == nil {
		value, err := backend.Content(ctx, id)
		if err != nil {
			f.replace(driverprotocol.Content{Error: err.Error()})
			wait(ctx, contentRetryInterval)
			return
		}
		f.replace(value)
		wait(ctx, contentPollInterval)
	}
}

func wait(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (f *contentFeed) apply(event driverprotocol.ContentEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.value = driverprotocol.ApplyContent(f.value, event)
	size := len(event.Text)
	if event.Tool != nil {
		size += len(event.Tool.Input) + len(event.Tool.Output)
	}
	if event.Type == driverprotocol.ContentSnapshot || len(f.events) >= maxFeedEvents || f.bytes+size > maxFeedBytes {
		f.epoch++
		f.events, f.bytes = nil, 0
	} else {
		f.events = append(f.events, event)
		f.bytes += size
	}
	f.notify()
}

// replace sets a whole snapshot, from polling or an error, when it differs.
func (f *contentFeed) replace(value driverprotocol.Content) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.epoch > 0 && reflect.DeepEqual(value, f.value) {
		return
	}
	f.epoch++
	f.value = value
	f.events, f.bytes = nil, 0
	f.notify()
}

func (f *contentFeed) notify() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// feedCursor is how far one tab has followed a feed.
type feedCursor struct {
	epoch uint64
	sent  int
}

// next returns what a tab at cursor still needs: a snapshot when the feed was
// replaced since, otherwise the newer events (possibly none). It also returns
// the current snapshot and a channel closed on the next change.
func (f *contentFeed) next(cursor feedCursor) (snapshot *driverprotocol.Content, events []driverprotocol.ContentEvent, current driverprotocol.Content, after feedCursor, changed <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current = f.value
	current.Tool = cloneContentTool(current.Tool)
	after = feedCursor{epoch: f.epoch, sent: len(f.events)}
	if f.epoch == 0 {
		return nil, nil, current, cursor, f.changed
	}
	if cursor.epoch != f.epoch {
		return &current, nil, current, after, f.changed
	}
	return nil, append([]driverprotocol.ContentEvent(nil), f.events[cursor.sent:]...), current, after, f.changed
}

func cloneContentTool(tool *driverprotocol.ContentTool) *driverprotocol.ContentTool {
	if tool == nil {
		return nil
	}
	copy := *tool
	return &copy
}
