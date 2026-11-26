package daemonapi_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
)

const missingItemID = "01M5ZZZZZZZZZZZZZZZZZZZZZZ"

// Jobs are read, listed, watched and cancelled through the API; a finished
// job stays readable.
func TestJobsThroughTheAPI(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repo := f.Repository(t, "test")
	item, _ := activeItem(t, f, repo, "jobs")
	other, _ := activeItem(t, f, repo, "other")
	jobs := call(t, f.Clients.Jobs.ListJobs, &v1.ListJobsRequest{ItemId: item.Id}).Jobs
	if len(jobs) == 0 {
		t.Fatal("the item's checkout job is not listed")
	}
	job := jobs[0]
	for _, listed := range jobs {
		if listed.ItemId != item.Id {
			t.Fatalf("an item's jobs list another's: %v", listed)
		}
	}

	t.Run("get and list", func(t *testing.T) {
		got := call(t, f.Clients.Jobs.GetJob, &v1.GetJobRequest{JobId: job.Id}).Job
		if got.State != v1.JobState_JOB_STATE_SUCCEEDED || got.ItemId != item.Id || got.Kind == "" {
			t.Fatalf("job: %v", got)
		}
		all := call(t, f.Clients.Jobs.ListJobs, &v1.ListJobsRequest{}).Jobs
		if !accessHasJob(all, job.Id) || !accessHasJob(all, call(t, f.Clients.Jobs.ListJobs, &v1.ListJobsRequest{ItemId: other.Id}).Jobs[0].Id) {
			t.Fatalf("every job: %v", all)
		}
		if running := call(t, f.Clients.Jobs.ListJobs, &v1.ListJobsRequest{RunningOnly: true}).Jobs; accessHasJob(running, job.Id) {
			t.Fatalf("running jobs list a finished one: %v", running)
		}
		refused(t, connect.CodeNotFound, f.Clients.Jobs.GetJob, &v1.GetJobRequest{JobId: "missing"})
	})

	t.Run("a late watcher gets the terminal snapshot", func(t *testing.T) {
		stream, err := f.Clients.Jobs.WatchJob(t.Context(), connect.NewRequest(&v1.WatchJobRequest{JobId: job.Id}))
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		if !stream.Receive() || stream.Msg().Job.State != v1.JobState_JOB_STATE_SUCCEEDED || stream.Receive() || stream.Err() != nil {
			t.Fatalf("late watcher: %v %v", stream.Msg(), stream.Err())
		}
	})

	t.Run("cancelling a finished job changes nothing", func(t *testing.T) {
		if got := call(t, f.Clients.Jobs.CancelJob, &v1.CancelJobRequest{JobId: job.Id}).Job; got.State != v1.JobState_JOB_STATE_SUCCEEDED {
			t.Fatalf("cancelled: %v", got)
		}
		refused(t, connect.CodeNotFound, f.Clients.Jobs.CancelJob, &v1.CancelJobRequest{JobId: "missing"})
	})
}

// The change feed opens with a cursor and no changes; a client resuming
// from a cursor hears what changed since.
func TestEventsChangeFeedResumesFromItsCursor(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	item := newItem(t, f, f.Repository(t, "test"), "watched").Id
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	first, err := f.Clients.Events.WatchChanges(ctx, connect.NewRequest(&v1.WatchChangesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Receive() || first.Msg().Cursor == "" || first.Msg().All || len(first.Msg().ItemIds) != 0 {
		t.Fatalf("opening: %v %v", first.Msg(), first.Err())
	}
	cursor := first.Msg().Cursor
	first.Close()
	call(t, f.Clients.Items.AddNote, &v1.AddNoteRequest{ItemId: item, Text: "while away"})
	resumed, err := f.Clients.Events.WatchChanges(ctx, connect.NewRequest(&v1.WatchChangesRequest{After: cursor}))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	for {
		if !resumed.Receive() {
			t.Fatalf("no change after the cursor: %v", resumed.Err())
		}
		if slices.Contains(resumed.Msg().ItemIds, item) {
			break
		}
	}
	if resumed.Msg().Cursor == cursor {
		t.Fatal("the cursor did not advance")
	}
}

// One Watch stream carries what a view follows: changes, jobs as they move,
// an item's live content and heartbeats; it keeps the observation beat
// running only while open.
func TestEventsWatchMultiplexes(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	f.API().SetHeartbeatInterval(300 * time.Millisecond)
	repo := f.Repository(t, "test")
	id := newItem(t, f, repo, "watched").Id
	refusedStream := func() error {
		stream, err := f.Clients.Events.Watch(t.Context(), connect.NewRequest(&v1.WatchRequest{ItemId: missingItemID}))
		if err == nil {
			for stream.Receive() {
			}
			err = stream.Err()
		}
		return err
	}
	if err := refusedStream(); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("watching a missing item: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	watchCtx, stop := context.WithCancel(ctx)
	stream, err := f.Clients.Events.Watch(watchCtx, connect.NewRequest(&v1.WatchRequest{ItemId: id}))
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
	// It opens with a change cursor, the running jobs, and, the agent not
	// running, an empty content snapshot, in any order.
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
	// White-box: the observation beat has no API of its own.
	if watchers := f.API().Watchers(); watchers != 1 {
		t.Fatalf("watchers = %d", watchers)
	}
	call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})
	job := call(t, f.Clients.Workspaces.EnsureWorkspace, &v1.EnsureWorkspaceRequest{ItemId: id}).Job
	until("the job finished", func(e *v1.WatchResponse) bool {
		for _, j := range e.GetJobs().GetJobs() {
			if j.Id == job.Id && j.State == v1.JobState_JOB_STATE_SUCCEEDED {
				return true
			}
		}
		return false
	})
	call(t, f.Clients.Items.AddNote, &v1.AddNoteRequest{ItemId: id, Text: "changed"})
	until("the item's change", func(e *v1.WatchResponse) bool { return slices.Contains(e.GetChanges().GetItemIds(), id) })
	until("a heartbeat", func(e *v1.WatchResponse) bool { return e.GetHeartbeat() != nil })
	stop()
	for range events {
	}
	eventually(t, "the watch to close", func() bool { return f.API().Watchers() == 0 })
}

// Watches end while their parts are still sending: when the first part
// ends, the others may be mid-send. The daemon must refuse those sends
// rather than write to a finished response, which crashed it.
func TestEventsWatchSurvivesEndingWhileChangesArrive(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	f.API().SetHeartbeatInterval(30 * time.Millisecond)
	repo := f.Repository(t, "test")
	id := newItem(t, f, repo, "churned").Id
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	churned := make(chan struct{})
	go func() {
		defer close(churned)
		for i := 0; ctx.Err() == nil; i++ {
			_, _ = f.Clients.Items.AddNote(ctx, connect.NewRequest(&v1.AddNoteRequest{ItemId: id, Text: fmt.Sprintf("change %d", i)}))
		}
	}()
	var wg sync.WaitGroup
	for watcher := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := range 10 {
				watchCtx, stop := context.WithTimeout(ctx, time.Duration(1+(watcher+round)%7)*time.Millisecond)
				if stream, err := f.Clients.Events.Watch(watchCtx, connect.NewRequest(&v1.WatchRequest{ItemId: id})); err == nil {
					for stream.Receive() {
					}
					_ = stream.Close()
				}
				stop()
			}
		}()
	}
	wg.Wait()
	cancel()
	<-churned
	call(t, f.Clients.System.GetInfo, &v1.GetInfoRequest{})
	eventually(t, "the watches to close", func() bool { return f.API().Watchers() == 0 })
}
