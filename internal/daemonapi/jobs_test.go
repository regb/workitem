package daemonapi

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/plugin"
)

func jobState(t *testing.T, f *fixture, id string, state v1.JobState) *v1.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		value, err := f.clients.Jobs.GetJob(context.Background(), connect.NewRequest(&v1.GetJobRequest{JobId: id}))
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
func TestJobApprovalIsBoundToChallengeAndFileContent(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "approval.txt")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	job := f.api.jobs.start("approval_test", "", "", func(ctx context.Context, _ *jobRun) error {
		allowed, err := plugin.ContextApprover(ctx)(ctx, plugin.Approval{Question: "Trust this file?", Path: path})
		if err != nil {
			return err
		}
		if !allowed {
			return precondition("approval denied")
		}
		return nil
	})
	first := jobState(t, f, job.Id, v1.JobState_JOB_STATE_WAITING_FOR_APPROVAL)
	if first.Approval == nil || first.Approval.Digest == "" || first.Approval.ExpiresAt == nil {
		t.Fatal(first)
	}
	_, err := f.clients.Jobs.AnswerApproval(context.Background(), connect.NewRequest(&v1.AnswerApprovalRequest{JobId: job.Id, ChallengeId: "wrong", Decision: v1.ApprovalDecision_APPROVAL_DECISION_ALLOW}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("wrong challenge: %v", err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = f.clients.Jobs.AnswerApproval(context.Background(), connect.NewRequest(&v1.AnswerApprovalRequest{JobId: job.Id, ChallengeId: first.Approval.ChallengeId, Decision: v1.ApprovalDecision_APPROVAL_DECISION_ALLOW}))
	if err != nil {
		t.Fatal(err)
	}
	var next *v1.Job
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		next = jobState(t, f, job.Id, v1.JobState_JOB_STATE_WAITING_FOR_APPROVAL)
		if next.Approval.ChallengeId != first.Approval.ChallengeId {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if next.Approval.ChallengeId == first.Approval.ChallengeId || next.Approval.Digest == first.Approval.Digest {
		t.Fatal("changed content reused approval", next)
	}
	_, err = f.clients.Jobs.AnswerApproval(context.Background(), connect.NewRequest(&v1.AnswerApprovalRequest{JobId: job.Id, ChallengeId: first.Approval.ChallengeId, Decision: v1.ApprovalDecision_APPROVAL_DECISION_ALLOW}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("old challenge replay: %v", err)
	}
	_, err = f.clients.Jobs.AnswerApproval(context.Background(), connect.NewRequest(&v1.AnswerApprovalRequest{JobId: job.Id, ChallengeId: next.Approval.ChallengeId, Decision: v1.ApprovalDecision_APPROVAL_DECISION_ALLOW}))
	if err != nil {
		t.Fatal(err)
	}
	jobState(t, f, job.Id, v1.JobState_JOB_STATE_SUCCEEDED)
}
func TestCancelJobRetainsPartialOutcome(t *testing.T) {
	f := newFixture(t)
	job := f.api.jobs.start("cancel_test", "", "", func(ctx context.Context, j *jobRun) error {
		path := filepath.Join(f.root, "retained")
		if err := os.WriteFile(path, []byte("durable"), 0600); err != nil {
			return err
		}
		_, err := plugin.ContextApprover(ctx)(ctx, plugin.Approval{Question: "Continue?", Path: path})
		return partial(err, "", "created retained file")
	})
	jobState(t, f, job.Id, v1.JobState_JOB_STATE_WAITING_FOR_APPROVAL)
	if _, err := f.clients.Jobs.CancelJob(context.Background(), connect.NewRequest(&v1.CancelJobRequest{JobId: job.Id})); err != nil {
		t.Fatal(err)
	}
	final := jobState(t, f, job.Id, v1.JobState_JOB_STATE_CANCELLED)
	if final.Partial == nil || len(final.Partial.Completed) != 1 || final.Partial.Completed[0] != "created retained file" {
		t.Fatal(final)
	}
	if data, err := os.ReadFile(filepath.Join(f.root, "retained")); err != nil || string(data) != "durable" {
		t.Fatalf("retained step lost: %s %v", data, err)
	}
}
func TestJobWatcherDisconnectDoesNotCancelTheOperation(t *testing.T) {
	f := newFixture(t)
	finish := make(chan struct{})
	job := f.api.jobs.start("watch_test", "", "", func(ctx context.Context, _ *jobRun) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-finish:
			return nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := f.clients.Jobs.WatchJob(ctx, connect.NewRequest(&v1.WatchJobRequest{JobId: job.Id}))
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
	late, err := f.clients.Jobs.WatchJob(context.Background(), connect.NewRequest(&v1.WatchJobRequest{JobId: job.Id}))
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	if !late.Receive() || late.Msg().Job.State != v1.JobState_JOB_STATE_SUCCEEDED || late.Receive() {
		t.Fatalf("late watcher missed terminal snapshot: %v", late.Err())
	}
}
func TestDifferentIdempotencyKeysDoNotCoalesceJobs(t *testing.T) {
	f := newFixture(t)
	finish := make(chan struct{})
	var started atomic.Int32
	run := func(ctx context.Context, _ *jobRun) error {
		started.Add(1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-finish:
			return nil
		}
	}
	first := f.api.jobs.startKey("same_kind", "one", "", "", run)
	same := f.api.jobs.startKey("same_kind", "one", "", "", run)
	second := f.api.jobs.startKey("same_kind", "two", "", "", run)
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
func TestRegistryShutdownRacesWithNewJobs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	registry := newJobRegistry(ctx)
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			registry.start("race", "", "", func(ctx context.Context, _ *jobRun) error { <-ctx.Done(); return ctx.Err() })
		}()
	}
	cancel()
	registry.wait()
	group.Wait()
}
