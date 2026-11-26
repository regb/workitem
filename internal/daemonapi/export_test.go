package daemonapi

import (
	"context"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/credential"
	"github.com/regb/workitem/internal/model"
)

// White-box hooks for the tests of package daemonapi_test, which run against
// an apitest daemon. Each is an internal of the server that such a test
// depends on: a test that uses one checks the server's implementation, not
// only its API.

// Jobs.

type JobRun = jobRun

var (
	TerminalJob = terminalJob
	Partial     = partial
)

func (j *JobRun) Change(change func(*v1.Job)) { j.change(change) }
func (j *JobRun) Phase(phase string)          { j.phase(phase) }

// StartJob runs a job of kind on the server's registry.
func (s *Server) StartJob(kind, item, repo string, run func(context.Context, *JobRun) error) *v1.Job {
	return s.jobs.start(kind, item, repo, run)
}

// StartJobKey runs a job of kind that coalesces with others of its key.
func (s *Server) StartJobKey(kind, key, item, repo string, run func(context.Context, *JobRun) error) *v1.Job {
	return s.jobs.startKey(kind, key, item, repo, run)
}

// JobSnapshot is a job's current state.
func (s *Server) JobSnapshot(id string) (*v1.Job, error) {
	run, err := s.jobs.get(id)
	if err != nil {
		return nil, err
	}
	value, _ := run.snapshot()
	return value, nil
}

// OnJobItemChanged replaces how the registry wakes an item's change readers.
func (s *Server) OnJobItemChanged(changed func(itemID string)) { s.jobs.itemChanged = changed }

// Access.

func (s *Server) Viewable(ctx context.Context, itemIDs []string) []string {
	return s.viewable(ctx, itemIDs)
}
func (s *Server) ViewableJobs(ctx context.Context, jobs []*v1.Job) []*v1.Job {
	return s.viewableJobs(ctx, jobs)
}

// Messages and control.

var MessageCommandID = messageCommandID

func (s *Server) MessageEnvelope(ctx context.Context, itemID, sender string) (model.Envelope, error) {
	return s.messageEnvelope(ctx, itemID, sender)
}
func (s *Server) MessageReceiptResult(receipt coordinator.APIMessageReceipt) (bool, error) {
	return s.messageReceiptResult(receipt)
}
func (s *Server) WaitMessageConfirmation(ctx context.Context, itemID, runtimeID, commandID string) error {
	return s.waitMessageConfirmation(ctx, itemID, runtimeID, commandID)
}

const (
	ScheduleClient = scheduleClient
	FollowUpClient = followUpClient
)

func (s *Server) ActOnItself(ctx context.Context, itemID, client, label string, origin model.Origin) (context.Context, func(job *v1.Job)) {
	return s.actOnItself(ctx, itemID, client, label, origin)
}
func (s *Server) ControlLeaseStarting(ctx context.Context, itemID, leaseID string, takeVacant, start bool) (*model.ControlLease, error) {
	return s.controlLeaseStarting(ctx, itemID, leaseID, takeVacant, start)
}

// Schedules.

var PartialItem = partialItem

func (s *Server) RunDueSchedules(now time.Time) { s.runDueSchedules(now) }

// Watch.

// SetHeartbeatInterval sets how often the server's Watch streams beat.
func (s *Server) SetHeartbeatInterval(interval time.Duration) { s.heartbeat.Store(int64(interval)) }

// Watchers counts open watch streams.
func (s *Server) Watchers() int {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	return s.watchers
}

// Usage.

const (
	UsageInterval = usageInterval
	UsageIdle     = usageIdle
	UsageFresh    = usageFresh
)

// SetUsageCheck replaces how the server checks usage.
func (s *Server) SetUsageCheck(run func(ctx context.Context, refresh bool) (*v1.GetUsageResponse, error)) {
	s.usage.run = run
}

// SetUsageResult stores result as the usage checked at checkedAt; a nil
// result keeps the stored one.
func (s *Server) SetUsageResult(result *v1.GetUsageResponse, checkedAt time.Time) {
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	if result != nil {
		s.usage.result = result
	}
	s.usage.checkedAt = checkedAt
}

// UsageChecking reports whether a usage check runs.
func (s *Server) UsageChecking() bool {
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	return s.usage.running != nil
}

// SetLastActivity records the API's last use at when.
func (s *Server) SetLastActivity(when time.Time) { s.usage.lastActivity.Store(when.Unix()) }

func (s *Server) MaybeCheckUsage(now time.Time)       { s.maybeCheckUsage(now) }
func (s *Server) UsageSnapshot() *v1.GetUsageResponse { return s.usageSnapshot() }
func (s *Server) ClearUsageBlocker(ctx context.Context, m model.Manifest, blocker model.Blocker, now time.Time) {
	s.clearUsageBlocker(ctx, m, blocker, now)
}

// AsItemAgent is ctx of a call made with itemID's agent's credential.
func AsItemAgent(ctx context.Context, itemID string) context.Context {
	return context.WithValue(ctx, identityKey{}, identity{principal: principal{kind: credential.Item, itemID: itemID}})
}
