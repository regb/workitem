package main

import (
	"connectrpc.com/connect"
	"context"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"strings"
	"testing"
	"time"
)

func (f *binaryFixture) restartJobs(repository, finished string) {
	f.t.Run("durable_jobs_after_restart", func(t *testing.T) {
		copy := *f
		copy.t = t
		f := &copy
		f.run("repo", "settings", "set", "--repo", repository, "plugins.setup.run", "sleep 60")
		// Plugin settings are cached by the existing daemon host. Start a fresh
		// host before this cancellation test; cache invalidation is separate work.
		f.run("daemon", "stop")
		f.run("daemon", "start")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		created, err := f.clients.Lifecycle.CreateAndStartItem(ctx, connect.NewRequest(&v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: repository, Title: "Interrupted setup"}, Start: &v1.StartOptions{Mode: v1.AgentMode_AGENT_MODE_HEADLESS, SendNothing: true}}))
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			status, err := f.clients.Workspaces.GetWorkspace(ctx, connect.NewRequest(&v1.GetWorkspaceRequest{ItemId: created.Msg.Item.Id}))
			if err == nil && status.Msg.Setup != nil && status.Msg.Setup.State == v1.SetupState_SETUP_STATE_RUNNING {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("setup never started: %v %v", status, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		f.run("daemon", "stop")
		f.run("daemon", "start")
		for _, jobID := range []string{finished, created.Msg.Job.Id} {
			response, err := f.clients.Jobs.GetJob(ctx, connect.NewRequest(&v1.GetJobRequest{JobId: jobID}))
			if err != nil {
				t.Fatal(err)
			}
			if jobID == finished {
				if response.Msg.Job.State != v1.JobState_JOB_STATE_SUCCEEDED {
					t.Fatal(response.Msg)
				}
			} else {
				if response.Msg.Job.State != v1.JobState_JOB_STATE_FAILED || !strings.Contains(response.Msg.Job.Error, "interrupted by a daemon restart") {
					t.Fatal(response.Msg)
				}
			}
		}
		f.run("repo", "settings", "unset", "--repo", repository, "plugins.setup.run")
		f.run("close", created.Msg.Item.Id, "--force")
	})
}
