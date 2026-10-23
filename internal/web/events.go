package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/model"
)

// Event stream timing. Live output is pushed as the driver's content feed
// changes. The list and conversations are re-read when the daemon reports
// that their items changed, or when the content shows a message saved or the
// activity changed, and at the latest after changesFallback, for state that
// changes without events (worktrees, process exits, lease expiry). A daemon
// that cannot report changes is polled every listPollInterval. Background
// starts are sent as they change.
const (
	changesFallback  = 10 * time.Second
	listPollInterval = 1500 * time.Millisecond
	streamLife       = 10 * time.Minute
	// keepAliveInterval keeps idle streams open through proxies.
	keepAliveInterval = 15 * time.Second
)

// Frame is one server-sent event of the per-tab stream. List, creations, chat
// and live frames carry full replacement snapshots, so a reconnecting tab
// loses nothing. Content frames carry the content events that follow the
// last live snapshot, in order (see driverprotocol.ApplyContent).
type Frame struct {
	Type      string                        `json:"type"` // list, creations, chat, live, or content
	List      *List                         `json:"list,omitempty"`
	Creations []Creation                    `json:"creations,omitempty"`
	Chat      *Chat                         `json:"chat,omitempty"`
	Live      *driverprotocol.Content       `json:"live,omitempty"`
	Events    []driverprotocol.ContentEvent `json:"events,omitempty"`
	Error     string                        `json:"error,omitempty"`
}

// listHub follows changes for all open tabs while at least one is
// connected: it re-reads the work list once per change and publishes it when
// it differs, and counts changes per item so each tab re-reads only its own
// conversation.
type listHub struct {
	backend     Backend
	mu          sync.Mutex
	subscribers int
	encoded     []byte
	// version counts changes; items holds the version that last changed each
	// item, and all the version at which every item may have changed.
	version uint64
	items   map[string]uint64
	all     uint64
	wake    chan struct{}
	stop    chan struct{}
}

func newListHub(backend Backend) *listHub {
	return &listHub{backend: backend, items: map[string]uint64{}, wake: make(chan struct{})}
}

func (h *listHub) subscribe() func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subscribers++
	if h.subscribers == 1 {
		h.stop = make(chan struct{})
		go h.run(h.stop)
	}
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.subscribers--
		if h.subscribers == 0 {
			close(h.stop)
			h.encoded = nil // A later subscriber starts from a fresh read.
		}
	}
}

// current returns the latest encoded list frame (nil before the first read),
// the change version of one item, and a channel closed on the next change.
func (h *listHub) current(item string) ([]byte, uint64, <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.encoded, max(h.items[item], h.all), h.wake
}

func (h *listHub) run(stop <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-stop; cancel() }()
	epoch, after := "", uint64(0)
	var inspected time.Time
	for ctx.Err() == nil {
		changes, err := h.backend.Changes(ctx, epoch, after, changesFallback)
		if ctx.Err() != nil {
			return
		}
		// A wait that ends without changes is the fallback beat: anything may
		// have changed without an event, so the daemon inspects again. A
		// change was already observed; re-inspecting every worktree for each
		// event of a running turn would hold up the daemon.
		beat := len(changes.Items) == 0
		if err != nil {
			// The daemon cannot report changes: poll, inspecting only as
			// often as the fallback beat would.
			changes = Changes{All: true}
			wait(ctx, listPollInterval)
			beat = time.Since(inspected) >= changesFallback
		} else {
			epoch, after = changes.Epoch, changes.Revision
		}
		if beat {
			inspected = time.Now()
		}
		h.refresh(ctx, stop, beat)
		if beat {
			changes.All = true
		}
		h.touch(changes)
	}
}

// refresh re-reads the work list and records it when it differs. A run the
// last tab stopped records nothing: a later subscriber starts a run of its
// own, which this late result would otherwise overwrite.
func (h *listHub) refresh(ctx context.Context, stop <-chan struct{}, inspect bool) {
	read, cancel := context.WithTimeout(ctx, 15*time.Second)
	list, err := h.backend.List(read, inspect)
	cancel()
	frame := Frame{Type: "list", List: &list}
	if err != nil {
		frame = Frame{Type: "list", Error: err.Error()}
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-stop:
		return
	default:
	}
	if !bytes.Equal(encoded, h.encoded) {
		h.encoded = encoded
		h.notify()
	}
}

// touch records which items changed and wakes the tabs.
func (h *listHub) touch(changes Changes) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.version++
	if changes.All {
		h.all = h.version
		clear(h.items)
	} else {
		for _, item := range changes.Items {
			h.items[item] = h.version
		}
	}
	h.notify()
}

func (h *listHub) notify() {
	close(h.wake)
	h.wake = make(chan struct{})
}

// events streams server-sent frames for one tab: the work list and creation
// receipts always, plus the open conversation and its live output when the
// tab names an item. Frames are sent only when their content changes.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("item")
	if id != "" && !model.ValidID(id) {
		badRequest(w, errors.New("invalid item"))
		return
	}
	if id != "" {
		if err := s.backend.CheckItem(r.Context(), id); err != nil {
			reply(w, struct{}{}, err)
			return
		}
	}
	select {
	case s.streams <- struct{}{}:
		defer func() { <-s.streams }()
	default:
		http.Error(w, "too many streams", http.StatusTooManyRequests)
		return
	}
	flush, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	defer s.lists.subscribe()()
	var feed *contentFeed
	if id != "" {
		var leave func()
		feed, leave = s.contents.join(id)
		defer leave()
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	ctx := r.Context()
	// write sends one SSE event; a nil payload sends a keep-alive comment.
	write := func(payload []byte) bool {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
		event := []byte(": keep-alive\n\n")
		if payload != nil {
			event = append(append([]byte("data: "), payload...), '\n', '\n')
		}
		if _, err := w.Write(event); err != nil {
			return false
		}
		flush.Flush()
		return true
	}
	send := func(frame Frame) bool {
		encoded, err := json.Marshal(frame)
		return err == nil && write(encoded)
	}
	// sendChanged writes a frame unless its key matches the last one sent.
	sendChanged := func(last *[]byte, frame Frame, key []byte) bool {
		if key == nil {
			key, _ = json.Marshal(frame)
		}
		if bytes.Equal(key, *last) {
			return true
		}
		*last = key
		return send(frame)
	}
	var sentList, sentCreations, sentChat []byte
	var chatVersion uint64
	chatRead := false
	var leaf, activity string
	var cursor feedCursor
	lifetime := time.NewTimer(streamLife)
	defer lifetime.Stop()
	keepAlive := time.NewTicker(keepAliveInterval)
	defer keepAlive.Stop()
	for {
		list, version, listChanged := s.lists.current(id)
		if list != nil && !bytes.Equal(list, sentList) {
			if !write(list) {
				return
			}
			sentList = list
		}
		creations, creationsChanged := s.background.snapshot()
		if !sendChanged(&sentCreations, Frame{Type: "creations", Creations: creations}, nil) {
			return
		}
		// A nil channel never fires: tabs without an item wait on the rest.
		var contentChanged <-chan struct{}
		if feed != nil {
			snapshot, events, live, after, changed := feed.next(cursor)
			cursor, contentChanged = after, changed
			if snapshot != nil && !send(Frame{Type: "live", Live: snapshot}) || len(events) > 0 && !send(Frame{Type: "content", Events: events}) {
				return
			}
			if !chatRead || version != chatVersion || live.LeafID != leaf || live.Activity != activity {
				leaf, activity, chatVersion, chatRead = live.LeafID, live.Activity, version, true
				frame := Frame{Type: "chat"}
				// A stalled daemon must not stall the stream: no keep-alive
				// or other frame goes out while the chat is read.
				read, cancel := context.WithTimeout(ctx, 15*time.Second)
				chat, err := s.chatSnapshot(read, id)
				cancel()
				if err != nil {
					frame.Error = err.Error()
				} else {
					frame.Chat = &chat
				}
				if !sendChanged(&sentChat, frame, chatKey(frame)) {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-lifetime.C:
			return // The tab reconnects and receives fresh snapshots.
		case <-listChanged:
		case <-contentChanged:
		case <-creationsChanged:
		case <-keepAlive.C:
			if !write(nil) {
				return
			}
		}
	}
}

// chatKey ignores lease renewal times, which change on every read while the
// tab holds control, so an unchanged conversation is not resent.
func chatKey(frame Frame) []byte {
	if frame.Chat != nil && frame.Chat.Control != nil && frame.Chat.Control.Status.Lease != nil {
		chat, control, lease := *frame.Chat, *frame.Chat.Control, *frame.Chat.Control.Status.Lease
		lease.RenewedAt, lease.ExpiresAt = time.Time{}, nil
		control.Status.Lease = &lease
		chat.Control = &control
		frame.Chat = &chat
	}
	key, _ := json.Marshal(frame)
	return key
}
