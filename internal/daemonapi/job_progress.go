package daemonapi

import (
	"context"
	"github.com/regb/workitem/internal/model"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func (j *jobRun) phase(phase string)  { j.change(func(value *v1.Job) { value.Phase = phase }) }
func (j *jobRun) logTail(tail string) { j.change(func(v *v1.Job) { v.LogTail = tail }) }
func (s *Server) startJob(request context.Context, kind, item, repo string, run func(context.Context, *jobRun) error) *v1.Job {
	return s.startJobKey(request, kind, kind, item, repo, run)
}

// startJobKey runs work that outlives the request as a job. The job acts as
// the request did: what it records names the request's origin.
func (s *Server) startJobKey(request context.Context, kind, key, item, repo string, run func(context.Context, *jobRun) error) *v1.Job {
	origin := model.OriginFrom(request)
	return s.jobs.startKey(kind, key, item, repo, func(ctx context.Context, j *jobRun) error {
		ctx = model.WithOrigin(ctx, origin)
		ctx, giveBack := s.withTransientLeases(ctx)
		defer giveBack()
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
		if err == nil && item != "" && agentStartingJobs[kind] {
			s.recordAgentStart(ctx, j, item)
		}
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

// recordAgentStart gives a job that started an item's agent that agent as
// its result.
func (s *Server) recordAgentStart(ctx context.Context, j *jobRun, itemID string) {
	agent, err := s.generation(ctx, itemID, 0)
	if err != nil || !agent.Online {
		return
	}
	result := &v1.AgentStartResult{Generation: agent.Generation, Mode: agent.Mode}
	j.change(func(v *v1.Job) { v.Result = &v1.Job_AgentStart{AgentStart: result} })
}
