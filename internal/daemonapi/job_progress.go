package daemonapi

import (
	"context"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func (j *jobRun) phase(phase string)  { j.change(func(value *v1.Job) { value.Phase = phase }) }
func (j *jobRun) logTail(tail string) { j.change(func(v *v1.Job) { v.LogTail = tail }) }
func (s *Server) startJob(kind, item, repo string, run func(context.Context, *jobRun) error) *v1.Job {
	return s.startJobKey(kind, kind, item, repo, run)
}
func (s *Server) startJobKey(kind, key, item, repo string, run func(context.Context, *jobRun) error) *v1.Job {
	return s.jobs.startKey(kind, key, item, repo, func(ctx context.Context, j *jobRun) error {
		done := make(chan struct{})
		finished := make(chan struct{})
		monitorSetup := item != "" && (kind == "start_item" || kind == "setup_workspace" || kind == "send_message")
		if monitorSetup {
			go func() {
				defer close(finished)
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-done:
						return
					case <-ctx.Done():
						return
					case <-ticker.C:
						setup := s.setup(ctx, item)
						snapshot, _ := j.snapshot()
						if setup != nil && setup.StartedAt != nil && !setup.StartedAt.AsTime().Before(snapshot.CreatedAt.AsTime()) {
							j.logTail(setup.LogTail)
						}
					}
				}
			}()
		} else {
			close(finished)
		}
		err := run(ctx, j)
		close(done)
		<-finished
		if monitorSetup {
			if setup := s.setup(ctx, item); setup != nil {
				snapshot, _ := j.snapshot()
				if setup.StartedAt != nil && !setup.StartedAt.AsTime().Before(snapshot.CreatedAt.AsTime()) {
					j.logTail(setup.LogTail)
				}
			}
		}
		return err
	})
}
