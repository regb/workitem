package daemonapi_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/daemonapi"
)

func TestSchedulesStartItemsLikeTheDashboard(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	repository := f.Repository(t, "test")
	put := func(s *v1.Schedule) (*v1.Schedule, error) {
		value, err := f.Clients.Schedules.PutSchedule(ctx, connect.NewRequest(&v1.PutScheduleRequest{Schedule: s}))
		if err != nil {
			return nil, err
		}
		return value.Msg.Schedule, nil
	}
	if _, err := put(&v1.Schedule{RepositoryId: repository, Cron: "0 9 * * *", Prompt: "p", AgentProfile: "missing", Enabled: true}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown profile: %v", err)
	}
	if _, err := put(&v1.Schedule{RepositoryId: repository, Cron: "every morning", Prompt: "p", AgentProfile: "test", Enabled: true}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad cron: %v", err)
	}
	if _, err := put(&v1.Schedule{RepositoryId: repository, Cron: "0 9 * * *", AgentProfile: "test", Enabled: true}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no prompt: %v", err)
	}
	if _, err := put(&v1.Schedule{RepositoryId: repository, Cron: "0 9 * * *", Prompt: "p", AgentProfile: "test", AgentSettings: map[string]string{"bogus": "x"}, Enabled: true}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a setting the driver refuses is refused on save: %v", err)
	}
	schedule, err := put(&v1.Schedule{RepositoryId: repository, Cron: "0 9 * * *", Title: "Triage {{date}}", Prompt: "Look at CI for {{repo}}", AgentProfile: "test", AgentSettings: map[string]string{"reasoning": "high"}, Labels: []string{"ci"}, Enabled: true})
	if err != nil || schedule.Id == "" || schedule.NextRunAt == nil {
		t.Fatalf("put: %v %v", schedule, err)
	}
	item := func(id string) *v1.Item {
		t.Helper()
		value, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id}))
		if err != nil {
			t.Fatalf("scheduled item %q: %v", id, err)
		}
		return value.Msg.Item
	}
	run, err := f.Clients.Schedules.RunSchedule(ctx, connect.NewRequest(&v1.RunScheduleRequest{Id: schedule.Id}))
	if err != nil && daemonapi.PartialItem(err) == "" {
		t.Fatal(err)
	}
	id := run.Msg.GetItemId()
	if id == "" {
		id = daemonapi.PartialItem(err)
	}
	created := item(id)
	if created.Title != "Triage "+time.Now().Format("2006-01-02") || created.AgentProfile != "test" || created.AgentSettings["reasoning"] != "high" || len(created.Labels) != 1 || created.Labels[0] != "ci" {
		t.Fatalf("run created: %v", created)
	}
	description, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: id, IncludeDescription: true}))
	if err != nil || description.Msg.Description == "" {
		t.Fatalf("description: %v %v", description, err)
	}
	// The daemon fires a due schedule, a few minutes late at most, and moves
	// it to its next run.
	due := schedule.NextRunAt.AsTime()
	f.API().RunDueSchedules(due.Add(4 * time.Minute))
	var fired *v1.Schedule
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		list, err := f.Clients.Schedules.ListSchedules(ctx, connect.NewRequest(&v1.ListSchedulesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if s := list.Msg.Schedules[0]; s.LastRunAt != nil && s.LastRunAt.AsTime().Equal(due) {
			fired = s
			break
		}
	}
	if fired == nil || fired.LastItemId == "" || fired.LastItemId == id || !fired.NextRunAt.AsTime().Equal(due.AddDate(0, 0, 1)) {
		t.Fatalf("due firing: %v", fired)
	}
	item(fired.LastItemId)
	// A run missed by more than five minutes is skipped.
	next := fired.NextRunAt.AsTime()
	f.API().RunDueSchedules(next.Add(10 * time.Minute))
	list, err := f.Clients.Schedules.ListSchedules(ctx, connect.NewRequest(&v1.ListSchedulesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if s := list.Msg.Schedules[0]; !s.LastRunAt.AsTime().Equal(due) || !s.NextRunAt.AsTime().Equal(next.AddDate(0, 0, 1)) {
		t.Fatalf("missed firing: %v", s)
	}
	if _, err := f.Clients.Schedules.DeleteSchedule(ctx, connect.NewRequest(&v1.DeleteScheduleRequest{Id: schedule.Id})); err != nil {
		t.Fatal(err)
	}
}
