package daemonapi

import (
	"context"
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"google.golang.org/protobuf/proto"
)

func TestJobsRecoverUnfinishedAndKeepFinished(t *testing.T) {
	root := t.TempDir()
	db, err := coordinator.OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	original := []*v1.Job{
		{Id: "complete", Kind: "test", State: v1.JobState_JOB_STATE_SUCCEEDED, CreatedAt: stamp(time.Now()), FinishedAt: stamp(time.Now())},
		{Id: "running", Kind: "test", ItemId: "subject", State: v1.JobState_JOB_STATE_RUNNING, CreatedAt: stamp(time.Now()), Phase: "retained phase", LogTail: "retained log", Partial: &v1.PartialOutcome{Completed: []string{"claimed workspace"}}},
		{Id: "queued", Kind: "test", State: v1.JobState_JOB_STATE_QUEUED, CreatedAt: stamp(time.Now())},
		{Id: "approval", Kind: "test", State: v1.JobState_JOB_STATE_WAITING_FOR_APPROVAL, CreatedAt: stamp(time.Now()), Approval: &v1.Approval{ChallengeId: "old"}},
	}
	for _, job := range original {
		raw, _ := proto.Marshal(job)
		var finished time.Time
		if terminalJob(job.State) {
			finished = job.FinishedAt.AsTime()
		}
		if err := db.SaveAPIJob(job.Id, raw, finished); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = coordinator.OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry, err := newPersistentJobRegistry(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.wait()
	for _, before := range original {
		entry, err := registry.get(before.Id)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := entry.snapshot()
		if before.Id == "complete" {
			if !proto.Equal(before, after) {
				t.Fatal(after)
			}
			continue
		}
		if after.State != v1.JobState_JOB_STATE_FAILED || after.Error != "interrupted by a daemon restart" || after.Approval != nil || after.FinishedAt == nil {
			t.Fatal(after)
		}
		if after.Phase != before.Phase || after.LogTail != before.LogTail || !proto.Equal(after.Partial, before.Partial) {
			t.Fatalf("lost retained state: %v", after)
		}
	}
	again, err := newPersistentJobRegistry(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer again.wait()
	if len(again.list("", false)) != 4 {
		t.Fatal("recovery was not persisted")
	}
}
func TestDaemonShutdownPersistsInterruptedJob(t *testing.T) {
	db, err := coordinator.OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	registry, err := newPersistentJobRegistry(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	job := registry.start("test", "", "", func(ctx context.Context, j *jobRun) error {
		j.phase("retained")
		close(ready)
		<-ctx.Done()
		return partial(ctx.Err(), "subject", "created durable step")
	})
	<-ready
	cancel()
	registry.wait()
	recovered, err := newPersistentJobRegistry(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.wait()
	entry, err := recovered.get(job.Id)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := entry.snapshot()
	if value.State != v1.JobState_JOB_STATE_FAILED || value.Error != "interrupted by a daemon restart" || value.Partial == nil {
		t.Fatal(value)
	}
}
func TestJobSummaryCorruptionFailsStartup(t *testing.T) {
	db, err := coordinator.OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SaveAPIJob("bad", []byte{255}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := newPersistentJobRegistry(context.Background(), db); err == nil {
		t.Fatal("malformed protobuf summary ignored")
	}
}
