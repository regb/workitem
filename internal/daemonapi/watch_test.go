package daemonapi

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// One Watch stream carries what a view follows: changes, jobs as they move,
// an item's live content and heartbeats; it keeps the observation beat
// running only while open.
func TestWatchMultiplexesChangesJobsContentAndHeartbeats(t *testing.T) {
	heartbeat := heartbeatInterval
	heartbeatInterval = 300 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = heartbeat })
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.repo(t), Title: "watched"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	watchCtx, stop := context.WithCancel(ctx)
	stream, err := f.clients.Events.Watch(watchCtx, connect.NewRequest(&v1.WatchRequest{ItemId: id}))
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan *v1.WatchResponse, 64)
	go func() {
		defer close(events)
		for stream.Receive() {
			events <- stream.Msg()
		}
	}()
	// until reads events until one matches, failing at the deadline.
	until := func(what string, match func(*v1.WatchResponse) bool) {
		t.Helper()
		for {
			select {
			case event, open := <-events:
				if !open {
					t.Fatalf("stream ended waiting for %s: %v", what, stream.Err())
				}
				if match(event) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("no %s", what)
			}
		}
	}
	// The stream opens with a change cursor, the running jobs, and, the agent
	// not running, an empty content snapshot, in any order.
	opened := map[string]bool{}
	until("the opening events", func(e *v1.WatchResponse) bool {
		switch {
		case e.GetChanges().GetCursor() != "":
			opened["changes"] = true
		case e.GetJobs().GetSnapshot():
			opened["jobs"] = true
		case e.GetContent().GetSnapshot() != nil:
			opened["content"] = true
		}
		return len(opened) == 3
	})
	f.api.watchMu.Lock()
	watchers := f.api.watchers
	f.api.watchMu.Unlock()
	if watchers != 1 {
		t.Fatalf("watchers = %d", watchers)
	}

	release := make(chan struct{})
	job := f.api.jobs.start("setup_workspace", id, "", func(ctx context.Context, j *jobRun) error {
		j.phase("setting up")
		<-release
		return nil
	})
	until("job phase", func(e *v1.WatchResponse) bool {
		for _, j := range e.GetJobs().GetJobs() {
			if j.Id == job.Id && j.Phase == "setting up" {
				return true
			}
		}
		return false
	})
	close(release)
	until("job finished", func(e *v1.WatchResponse) bool {
		for _, j := range e.GetJobs().GetJobs() {
			if j.Id == job.Id && j.State == v1.JobState_JOB_STATE_SUCCEEDED {
				return true
			}
		}
		return false
	})

	if _, err := f.clients.Items.AddNote(ctx, connect.NewRequest(&v1.AddNoteRequest{ItemId: id, Text: "changed"})); err != nil {
		t.Fatal(err)
	}
	until("item change", func(e *v1.WatchResponse) bool {
		for _, changed := range e.GetChanges().GetItemIds() {
			if changed == id {
				return true
			}
		}
		return false
	})
	until("heartbeat", func(e *v1.WatchResponse) bool { return e.GetHeartbeat() != nil })

	stop()
	for range events {
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		f.api.watchMu.Lock()
		watchers := f.api.watchers
		f.api.watchMu.Unlock()
		if watchers == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watchers = %d after the stream closed", watchers)
		}
	}
}

func TestWatchRefusesAMissingItem(t *testing.T) {
	f := newFixture(t)
	stream, err := f.clients.Events.Watch(context.Background(), connect.NewRequest(&v1.WatchRequest{ItemId: missingItemID}))
	if err == nil {
		for stream.Receive() {
		}
		err = stream.Err()
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing item: %v", err)
	}
}

const missingItemID = "01M5ZZZZZZZZZZZZZZZZZZZZZZ"
