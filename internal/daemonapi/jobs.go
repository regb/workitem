package daemonapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/plugin"
	"google.golang.org/protobuf/proto"
)

type jobRegistry struct {
	ctx     context.Context
	mu      sync.Mutex
	entries map[string]*jobRun
	wg      sync.WaitGroup
	closed  bool
	store   *coordinator.Database
	errMu   sync.Mutex
	err     error
}
type jobRun struct {
	mu         sync.Mutex
	key        string
	value      *v1.Job
	notify     chan struct{}
	answer     chan v1.ApprovalDecision
	cancel     context.CancelFunc
	save       func(*v1.Job) error
	persistErr error
}

func newJobRegistry(ctx context.Context) *jobRegistry {
	return &jobRegistry{ctx: ctx, entries: map[string]*jobRun{}}
}
func newPersistentJobRegistry(ctx context.Context, db *coordinator.Database) (*jobRegistry, error) {
	r := newJobRegistry(ctx)
	r.store = db
	records, err := db.APIJobs()
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		job := new(v1.Job)
		if err := proto.Unmarshal(record, job); err != nil {
			return nil, err
		}
		if job.Id == "" {
			return nil, errors.New("persisted job has no ID")
		}
		if !terminalJob(job.State) {
			job.State = v1.JobState_JOB_STATE_FAILED
			job.Error = "interrupted by a daemon restart"
			job.Approval = nil
			job.FinishedAt = stamp(time.Now())
			if err := r.persist(job); err != nil {
				return nil, err
			}
		}
		r.entries[job.Id] = &jobRun{value: job, notify: make(chan struct{}), cancel: func() {}, save: r.persist}
	}
	r.pruneLocked()
	return r, nil
}
func (r *jobRegistry) persist(value *v1.Job) error {
	if r.store == nil {
		return nil
	}
	data, err := proto.Marshal(value)
	if err == nil {
		var finished time.Time
		if terminalJob(value.State) {
			finished = value.FinishedAt.AsTime()
		}
		err = r.store.SaveAPIJob(value.Id, data, finished)
	}
	if err != nil {
		r.errMu.Lock()
		r.err = err
		r.errMu.Unlock()
	}
	return err
}
func (r *jobRegistry) healthError() error { r.errMu.Lock(); defer r.errMu.Unlock(); return r.err }
func (r *jobRegistry) pruneLocked() {
	type entry struct {
		id string
		at time.Time
	}
	var finished []entry
	for id, j := range r.entries {
		value, _ := j.snapshot()
		if terminalJob(value.State) {
			finished = append(finished, entry{id, value.FinishedAt.AsTime()})
		}
	}
	sort.Slice(finished, func(i, j int) bool {
		if finished[i].at.Equal(finished[j].at) {
			return finished[i].id > finished[j].id
		}
		return finished[i].at.After(finished[j].at)
	})
	for i := coordinator.APIJobHistoryLimit; i < len(finished); i++ {
		delete(r.entries, finished[i].id)
	}
}
func terminalJob(state v1.JobState) bool {
	return state == v1.JobState_JOB_STATE_SUCCEEDED || state == v1.JobState_JOB_STATE_FAILED || state == v1.JobState_JOB_STATE_CANCELLED
}
func (j *jobRun) snapshot() (*v1.Job, <-chan struct{}) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return proto.Clone(j.value).(*v1.Job), j.notify
}
func (j *jobRun) change(apply func(*v1.Job)) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.persistErr != nil {
		return false
	}
	next := proto.Clone(j.value).(*v1.Job)
	apply(next)
	if proto.Equal(next, j.value) {
		return true
	}
	if j.save != nil {
		if err := j.save(next); err != nil {
			j.persistErr = err
			j.cancel()
			next.State = v1.JobState_JOB_STATE_FAILED
			next.Error = "persist job summary: " + err.Error()
			next.FinishedAt = stamp(time.Now())
		}
	}
	j.value = next
	close(j.notify)
	j.notify = make(chan struct{})
	return j.persistErr == nil
}
func (r *jobRegistry) start(kind, item, repo string, run func(context.Context, *jobRun) error) *v1.Job {
	return r.startKey(kind, kind, item, repo, run)
}
func (r *jobRegistry) startKey(kind, key, item, repo string, run func(context.Context, *jobRun) error) *v1.Job {
	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil {
		value := &v1.Job{Id: id(), Kind: kind, ItemId: item, RepositoryId: repo, State: v1.JobState_JOB_STATE_CANCELLED, Error: "daemon is stopping", CreatedAt: stamp(time.Now()), FinishedAt: stamp(time.Now())}
		_ = r.persist(value)
		r.entries[value.Id] = &jobRun{value: value, notify: make(chan struct{}), cancel: func() {}, save: r.persist}
		r.mu.Unlock()
		return value
	}
	r.pruneLocked()
	for _, entry := range r.entries {
		value, _ := entry.snapshot()
		if entry.key == key && value.Kind == kind && value.ItemId == item && value.RepositoryId == repo && !terminalJob(value.State) {
			r.mu.Unlock()
			return value
		}
	}
	ctx, cancel := context.WithCancel(r.ctx)
	job := &jobRun{key: key, value: &v1.Job{Id: id(), Kind: kind, ItemId: item, RepositoryId: repo, State: v1.JobState_JOB_STATE_QUEUED, CreatedAt: stamp(time.Now())}, notify: make(chan struct{}), cancel: cancel, save: r.persist}
	if err := r.persist(job.value); err != nil {
		r.mu.Unlock()
		cancel()
		job.value.State = v1.JobState_JOB_STATE_FAILED
		job.value.Error = err.Error()
		job.value.FinishedAt = stamp(time.Now())
		return job.value
	}
	r.entries[job.value.Id] = job
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		defer cancel()
		if !job.change(func(v *v1.Job) {
			v.State = v1.JobState_JOB_STATE_RUNNING
			v.StartedAt = stamp(time.Now())
			v.Phase = kind
		}) {
			return
		}
		err := run(plugin.WithApprover(ctx, job.approve), job)
		job.change(func(v *v1.Job) {
			v.FinishedAt = stamp(time.Now())
			v.Approval = nil
			if err == nil {
				v.State = v1.JobState_JOB_STATE_SUCCEEDED
				v.Phase = "finished"
			} else {
				v.State = v1.JobState_JOB_STATE_FAILED
				v.Error = err.Error()
				if errors.Is(err, context.Canceled) {
					v.State = v1.JobState_JOB_STATE_CANCELLED
					if r.ctx.Err() != nil {
						v.State = v1.JobState_JOB_STATE_FAILED
						v.Error = "interrupted by a daemon restart"
					}
				}
				var ce *connect.Error
				if errors.As(err, &ce) {
					for _, d := range ce.Details() {
						if value, e := d.Value(); e == nil {
							if partial, ok := value.(*v1.PartialOutcome); ok {
								v.Partial = partial
							}
						}
					}
				}
			}
			if r.ctx.Err() != nil {
				v.State = v1.JobState_JOB_STATE_FAILED
				v.Error = "interrupted by a daemon restart"
			}
		})
		r.mu.Lock()
		r.pruneLocked()
		r.mu.Unlock()
	}()
	value, _ := job.snapshot()
	return value
}
func (r *jobRegistry) get(id string) (*jobRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	job := r.entries[id]
	if job == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("job not found"))
	}
	return job, nil
}
func (r *jobRegistry) wait() {
	r.mu.Lock()
	r.closed = true
	for _, job := range r.entries {
		job.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}
func (r *jobRegistry) list(item string, running bool) []*v1.Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	out := []*v1.Job{}
	for _, job := range r.entries {
		value, _ := job.snapshot()
		if (item == "" || value.ItemId == item) && (!running || !terminalJob(value.State)) {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.AsTime().After(out[j].CreatedAt.AsTime()) })
	return out
}
func digest(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (j *jobRun) approve(ctx context.Context, request plugin.Approval) (bool, error) {
	for {
		approval := &v1.Approval{ChallengeId: id(), Question: request.Question, Path: request.Path, Digest: digest(request.Path), ExpiresAt: stamp(time.Now().Add(10 * time.Minute))}
		answer := make(chan v1.ApprovalDecision, 1)
		j.mu.Lock()
		j.answer = answer
		j.mu.Unlock()
		if !j.change(func(v *v1.Job) { v.Approval = approval; v.State = v1.JobState_JOB_STATE_WAITING_FOR_APPROVAL }) {
			return false, j.persistErr
		}
		timer := time.NewTimer(time.Until(approval.ExpiresAt.AsTime()))
		var decision v1.ApprovalDecision
		select {
		case decision = <-answer:
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
			return false, precondition("approval expired")
		}
		timer.Stop()
		j.change(func(v *v1.Job) { v.Approval = nil; v.State = v1.JobState_JOB_STATE_RUNNING })
		if decision == v1.ApprovalDecision_APPROVAL_DECISION_DENY {
			return false, nil
		}
		if digest(request.Path) != approval.Digest {
			continue
		}
		return true, nil
	}
}
func (s *Server) GetJob(ctx context.Context, req *connect.Request[v1.GetJobRequest]) (*connect.Response[v1.GetJobResponse], error) {
	job, err := s.jobs.get(req.Msg.JobId)
	if err != nil {
		return nil, err
	}
	value, _ := job.snapshot()
	return response(&v1.GetJobResponse{Job: value}, nil)
}
func (s *Server) ListJobs(ctx context.Context, req *connect.Request[v1.ListJobsRequest]) (*connect.Response[v1.ListJobsResponse], error) {
	return response(&v1.ListJobsResponse{Jobs: s.jobs.list(req.Msg.ItemId, req.Msg.RunningOnly)}, nil)
}
func (s *Server) WatchJob(ctx context.Context, req *connect.Request[v1.WatchJobRequest], stream *connect.ServerStream[v1.WatchJobResponse]) error {
	job, err := s.jobs.get(req.Msg.JobId)
	if err != nil {
		return err
	}
	for {
		value, changed := job.snapshot()
		if err := stream.Send(&v1.WatchJobResponse{Job: value}); err != nil {
			return err
		}
		if terminalJob(value.State) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (s *Server) AnswerApproval(ctx context.Context, req *connect.Request[v1.AnswerApprovalRequest]) (*connect.Response[v1.AnswerApprovalResponse], error) {
	if req.Msg.Decision != v1.ApprovalDecision_APPROVAL_DECISION_ALLOW && req.Msg.Decision != v1.ApprovalDecision_APPROVAL_DECISION_DENY {
		return nil, invalid("approval decision is required")
	}
	job, err := s.jobs.get(req.Msg.JobId)
	if err != nil {
		return nil, err
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	approval := job.value.Approval
	if approval == nil || job.answer == nil || approval.ChallengeId != req.Msg.ChallengeId || !approval.ExpiresAt.AsTime().After(time.Now()) {
		return nil, aborted("approval challenge is stale or expired")
	}
	select {
	case job.answer <- req.Msg.Decision:
		job.answer = nil
	default:
		return nil, aborted("approval challenge was already answered")
	}
	return response(&v1.AnswerApprovalResponse{Job: proto.Clone(job.value).(*v1.Job)}, nil)
}
func (s *Server) CancelJob(ctx context.Context, req *connect.Request[v1.CancelJobRequest]) (*connect.Response[v1.CancelJobResponse], error) {
	job, err := s.jobs.get(req.Msg.JobId)
	if err != nil {
		return nil, err
	}
	job.cancel()
	value, _ := job.snapshot()
	return response(&v1.CancelJobResponse{Job: value}, nil)
}
func partial(err error, item string, completed ...string) error {
	if err == nil {
		return nil
	}
	ce := rpcError(err)
	var value *connect.Error
	if !errors.As(ce, &value) {
		value = connect.NewError(connect.CodeInternal, err)
	}
	detail, e := connect.NewErrorDetail(&v1.PartialOutcome{ItemId: item, Completed: completed})
	if e == nil {
		value.AddDetail(detail)
	}
	return value
}
