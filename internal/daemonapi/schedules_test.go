package daemonapi_test

import (
	"testing"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
)

// White-box: the daemon fires due schedules on its own clock, which no API
// call drives, so the test drives the server's RunDueSchedules.
func TestDueSchedulesFireLateAtMostAFewMinutes(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repository := f.Repository(t, "test")
	schedule := call(t, f.Clients.Schedules.PutSchedule, &v1.PutScheduleRequest{Schedule: &v1.Schedule{RepositoryId: repository, Cron: "0 9 * * *", Prompt: "Look at CI", AgentProfile: "test", Enabled: true}}).Schedule
	current := func() *v1.Schedule {
		t.Helper()
		return call(t, f.Clients.Schedules.ListSchedules, &v1.ListSchedulesRequest{}).Schedules[0]
	}
	// A run a few minutes late fires, and moves the schedule to its next run.
	due := schedule.NextRunAt.AsTime()
	f.API().RunDueSchedules(due.Add(4 * time.Minute))
	eventually(t, "the due run", func() bool {
		s := current()
		return s.LastRunAt != nil && s.LastRunAt.AsTime().Equal(due)
	})
	fired := current()
	if fired.LastItemId == "" || !fired.NextRunAt.AsTime().Equal(due.AddDate(0, 0, 1)) {
		t.Fatalf("due firing: %v", fired)
	}
	call(t, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: fired.LastItemId})
	// A run missed by more than five minutes is skipped.
	next := fired.NextRunAt.AsTime()
	f.API().RunDueSchedules(next.Add(10 * time.Minute))
	if s := current(); !s.LastRunAt.AsTime().Equal(due) || s.LastItemId != fired.LastItemId || !s.NextRunAt.AsTime().Equal(next.AddDate(0, 0, 1)) {
		t.Fatalf("missed firing: %v", s)
	}
}
