package daemonapi_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/daemonapi"
)

// The job registry as the API shows it. White-box: these jobs run on the
// server's registry, as no API operation can be held open on demand.
func TestJobRegistryThroughTheAPI(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	state := func(t *testing.T, id string, want v1.JobState) *v1.Job {
		t.Helper()
		var job *v1.Job
		eventually(t, "job state "+want.String(), func() bool {
			job = call(t, f.Clients.Jobs.GetJob, &v1.GetJobRequest{JobId: id}).Job
			return job.State == want
		})
		return job
	}
	// held runs until its context ends or finish is closed.
	held := func(finish chan struct{}) func(context.Context, *daemonapi.JobRun) error {
		return func(ctx context.Context, _ *daemonapi.JobRun) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-finish:
				return nil
			}
		}
	}

	t.Run("cancelling retains the partial outcome", func(t *testing.T) {
		path := filepath.Join(f.Root, "retained")
		job := f.API().StartJob("cancel_test", "", "", func(ctx context.Context, _ *daemonapi.JobRun) error {
			if err := os.WriteFile(path, []byte("durable"), 0600); err != nil {
				return err
			}
			<-ctx.Done()
			return daemonapi.Partial(ctx.Err(), "", "created retained file")
		})
		state(t, job.Id, v1.JobState_JOB_STATE_RUNNING)
		call(t, f.Clients.Jobs.CancelJob, &v1.CancelJobRequest{JobId: job.Id})
		final := state(t, job.Id, v1.JobState_JOB_STATE_CANCELLED)
		if final.Partial == nil || len(final.Partial.Completed) != 1 || final.Partial.Completed[0] != "created retained file" {
			t.Fatal(final)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "durable" {
			t.Fatalf("retained step lost: %s %v", data, err)
		}
	})

	t.Run("a watcher disconnecting does not cancel the job", func(t *testing.T) {
		finish := make(chan struct{})
		job := f.API().StartJob("watch_test", "", "", held(finish))
		ctx, cancel := context.WithCancel(t.Context())
		stream, err := f.Clients.Jobs.WatchJob(ctx, connect.NewRequest(&v1.WatchJobRequest{JobId: job.Id}))
		if err != nil || !stream.Receive() {
			t.Fatalf("watch: %v", err)
		}
		cancel()
		stream.Close()
		state(t, job.Id, v1.JobState_JOB_STATE_RUNNING)
		close(finish)
		state(t, job.Id, v1.JobState_JOB_STATE_SUCCEEDED)
	})

	t.Run("only the same key coalesces", func(t *testing.T) {
		finish := make(chan struct{})
		var started atomic.Int32
		run := held(finish)
		counted := func(ctx context.Context, j *daemonapi.JobRun) error { started.Add(1); return run(ctx, j) }
		first := f.API().StartJobKey("same_kind", "one", "", "", counted)
		same := f.API().StartJobKey("same_kind", "one", "", "", counted)
		second := f.API().StartJobKey("same_kind", "two", "", "", counted)
		if first.Id != same.Id || first.Id == second.Id {
			t.Fatalf("job keys: %v %v %v", first, same, second)
		}
		state(t, first.Id, v1.JobState_JOB_STATE_RUNNING)
		state(t, second.Id, v1.JobState_JOB_STATE_RUNNING)
		eventually(t, "both jobs to start", func() bool { return started.Load() == 2 })
		close(finish)
		state(t, first.Id, v1.JobState_JOB_STATE_SUCCEEDED)
		state(t, second.Id, v1.JobState_JOB_STATE_SUCCEEDED)
		if started.Load() != 2 {
			t.Fatalf("started %d jobs", started.Load())
		}
	})
}
