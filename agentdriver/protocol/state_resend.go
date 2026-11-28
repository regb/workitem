package driverprotocol

import (
	"context"
	"errors"
	"sync"
	"time"
)

// A runtime's state events, the agent starting and settling and its status,
// are what wi knows of it. Sent once, one wi was not there to take, while
// its daemon restarted, would leave the agent shown busy or starting until
// its next turn. StateResender sends a runtime's latest state event again
// until wi takes it, and gives up on it once wi takes a newer one, refuses
// it for good, or the runtime ends.

// stateResendEvery is how often an event wi did not take is sent again.
var stateResendEvery = time.Second

// StateResender sends a runtime's state events again when wi missed them.
type StateResender struct {
	ctx    context.Context
	socket string
	every  time.Duration
	mu     sync.Mutex
	// pending is the latest state event wi did not take, if any.
	pending *RuntimeEvent
	running bool
}

// NewStateResender sends again to socket, until ctx ends.
func NewStateResender(ctx context.Context, socket string) *StateResender {
	return &StateResender{ctx: ctx, socket: socket, every: stateResendEvery}
}

// IsStateEvent reports the events that say what state the agent is in.
func IsStateEvent(event RuntimeEvent) bool {
	return event.Type == EventAgentStarted || event.Type == EventAgentSettled || event.Type == EventRuntimeStatus
}

// Sent records how sending an event went. A state event wi did not take is
// sent again; one it took supersedes any still pending.
func (r *StateResender) Sent(event RuntimeEvent, err error) {
	if r == nil || !IsStateEvent(event) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil || definitive(err) {
		r.pending = nil
		return
	}
	copied := event
	r.pending = &copied
	if !r.running {
		r.running = true
		go r.resend()
	}
}

// definitive reports a refusal sending again cannot change, such as a
// runtime wi no longer recognizes.
func definitive(err error) bool {
	var rejected *EventRejectedError
	return errors.As(err, &rejected) && !rejected.Retryable
}

func (r *StateResender) resend() {
	ticker := time.NewTicker(r.every)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			r.mu.Lock()
			r.running = false
			r.mu.Unlock()
			return
		case <-ticker.C:
		}
		r.mu.Lock()
		event := r.pending
		r.mu.Unlock()
		if event == nil {
			r.mu.Lock()
			r.running = false
			r.mu.Unlock()
			return
		}
		ctx, cancel := context.WithTimeout(r.ctx, 2*time.Second)
		err := SubmitEvent(ctx, r.socket, *event)
		cancel()
		r.mu.Lock()
		// A newer state event replaced this one meanwhile: it is sent next.
		if r.pending != nil && r.pending.EventID == event.EventID && (err == nil || definitive(err)) {
			r.pending = nil
		}
		r.mu.Unlock()
	}
}
