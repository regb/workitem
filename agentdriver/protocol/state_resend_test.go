package driverprotocol

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/testutil"
)

// A state event wi missed while its daemon restarted reaches it once it is
// back, and only the latest one does.
func TestStateResenderDeliversTheLatestMissedState(t *testing.T) {
	every := stateResendEvery
	stateResendEvery = 20 * time.Millisecond
	t.Cleanup(func() { stateResendEvery = every })
	path := filepath.Join(testutil.ShortTempDir(t), "events.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resend := NewStateResender(ctx, path)
	missed := errors.New("connect: no such file or directory")
	resend.Sent(RuntimeEvent{EventID: "started", Type: EventAgentStarted}, missed)
	resend.Sent(RuntimeEvent{EventID: "progress", Type: "tool.started"}, missed)
	resend.Sent(RuntimeEvent{EventID: "settled", Type: EventAgentSettled}, missed)

	server, err := ListenEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	select {
	case request := <-server.Requests():
		if request.Event.EventID != "settled" {
			t.Fatalf("resent %q, want the latest state event", request.Event.EventID)
		}
		request.Respond(nil)
	case <-time.After(2 * time.Second):
		t.Fatal("the missed state event was not sent again")
	}
	select {
	case request := <-server.Requests():
		t.Fatalf("sent %q again after wi took it", request.Event.EventID)
	case <-time.After(100 * time.Millisecond):
	}
}

// A state event wi took supersedes one still waiting, and a definitive
// refusal ends the retries.
func TestStateResenderStopsWhenSuperseded(t *testing.T) {
	every := stateResendEvery
	stateResendEvery = 20 * time.Millisecond
	t.Cleanup(func() { stateResendEvery = every })
	path := filepath.Join(testutil.ShortTempDir(t), "events.sock")
	resend := NewStateResender(context.Background(), path)
	resend.Sent(RuntimeEvent{EventID: "started", Type: EventAgentStarted}, errors.New("down"))
	resend.Sent(RuntimeEvent{EventID: "settled", Type: EventAgentSettled}, nil)
	resend.Sent(RuntimeEvent{EventID: "status", Type: EventRuntimeStatus}, &EventRejectedError{Message: "unknown runtime"})
	server, err := ListenEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	select {
	case request := <-server.Requests():
		t.Fatalf("sent %q again", request.Event.EventID)
	case <-time.After(150 * time.Millisecond):
	}
}
