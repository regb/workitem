package daemonapi_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/daemonapi"
)

func jobState(t *testing.T, f *apitest.Fixture, id string, state v1.JobState) *v1.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		value, err := f.Clients.Jobs.GetJob(context.Background(), connect.NewRequest(&v1.GetJobRequest{JobId: id}))
		if err != nil {
			t.Fatal(err)
		}
		if value.Msg.Job.State == state {
			return value.Msg.Job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never reached %v: %v", state, value.Msg.Job)
		}
		time.Sleep(time.Millisecond)
	}
}
func TestCancelJobRetainsPartialOutcome(t *testing.T) {
	f := apitest.New(t)
	job := f.API().StartJob("cancel_test", "", "", func(ctx context.Context, j *daemonapi.JobRun) error {
		path := filepath.Join(f.Root, "retained")
		if err := os.WriteFile(path, []byte("durable"), 0600); err != nil {
			return err
		}
		<-ctx.Done()
		return daemonapi.Partial(ctx.Err(), "", "created retained file")
	})
	jobState(t, f, job.Id, v1.JobState_JOB_STATE_RUNNING)
	if _, err := f.Clients.Jobs.CancelJob(context.Background(), connect.NewRequest(&v1.CancelJobRequest{JobId: job.Id})); err != nil {
		t.Fatal(err)
	}
	final := jobState(t, f, job.Id, v1.JobState_JOB_STATE_CANCELLED)
	if final.Partial == nil || len(final.Partial.Completed) != 1 || final.Partial.Completed[0] != "created retained file" {
		t.Fatal(final)
	}
	if data, err := os.ReadFile(filepath.Join(f.Root, "retained")); err != nil || string(data) != "durable" {
		t.Fatalf("retained step lost: %s %v", data, err)
	}
}
func TestJobWatcherDisconnectDoesNotCancelTheOperation(t *testing.T) {
	f := apitest.New(t)
	finish := make(chan struct{})
	job := f.API().StartJob("watch_test", "", "", func(ctx context.Context, _ *daemonapi.JobRun) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-finish:
			return nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := f.Clients.Jobs.WatchJob(ctx, connect.NewRequest(&v1.WatchJobRequest{JobId: job.Id}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	cancel()
	stream.Close()
	jobState(t, f, job.Id, v1.JobState_JOB_STATE_RUNNING)
	close(finish)
	jobState(t, f, job.Id, v1.JobState_JOB_STATE_SUCCEEDED)
	late, err := f.Clients.Jobs.WatchJob(context.Background(), connect.NewRequest(&v1.WatchJobRequest{JobId: job.Id}))
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	if !late.Receive() || late.Msg().Job.State != v1.JobState_JOB_STATE_SUCCEEDED || late.Receive() {
		t.Fatalf("late watcher missed terminal snapshot: %v", late.Err())
	}
}
func TestDifferentIdempotencyKeysDoNotCoalesceJobs(t *testing.T) {
	f := apitest.New(t)
	finish := make(chan struct{})
	var started atomic.Int32
	run := func(ctx context.Context, _ *daemonapi.JobRun) error {
		started.Add(1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-finish:
			return nil
		}
	}
	first := f.API().StartJobKey("same_kind", "one", "", "", run)
	same := f.API().StartJobKey("same_kind", "one", "", "", run)
	second := f.API().StartJobKey("same_kind", "two", "", "", run)
	if first.Id != same.Id || first.Id == second.Id {
		t.Fatalf("job keys: %v %v %v", first, same, second)
	}
	jobState(t, f, first.Id, v1.JobState_JOB_STATE_RUNNING)
	jobState(t, f, second.Id, v1.JobState_JOB_STATE_RUNNING)
	if started.Load() != 2 {
		t.Fatal(started.Load())
	}
	close(finish)
	jobState(t, f, first.Id, v1.JobState_JOB_STATE_SUCCEEDED)
	jobState(t, f, second.Id, v1.JobState_JOB_STATE_SUCCEEDED)
}
