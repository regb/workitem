package cli

import (
	"context"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	webaccess "github.com/regb/workitem/internal/web"
)

func webSchedule(s *v1.Schedule) webaccess.Schedule {
	out := webaccess.Schedule{ID: s.Id, Revision: s.Revision, RepositoryID: s.RepositoryId, Cron: s.Cron, Title: s.Title, Prompt: s.Prompt, AgentProfile: s.AgentProfile, Labels: append([]string{}, s.Labels...), Enabled: s.Enabled, LastItemID: s.LastItemId, LastError: s.LastError}
	if s.NextRunAt != nil {
		at := s.NextRunAt.AsTime()
		out.NextRunAt = &at
	}
	if s.LastRunAt != nil {
		at := s.LastRunAt.AsTime()
		out.LastRunAt = &at
	}
	return out
}

func (b webBackend) Schedules(ctx context.Context) ([]webaccess.Schedule, error) {
	value, err := b.cfg.API.Schedules.ListSchedules(ctx, webRequest(ctx, &v1.ListSchedulesRequest{}))
	if err != nil {
		return nil, err
	}
	out := []webaccess.Schedule{}
	for _, s := range value.Msg.Schedules {
		out = append(out, webSchedule(s))
	}
	return out, nil
}

func (b webBackend) PutSchedule(ctx context.Context, s webaccess.Schedule) webaccess.Outcome {
	value, err := b.cfg.API.Schedules.PutSchedule(ctx, webRequest(ctx, &v1.PutScheduleRequest{Schedule: &v1.Schedule{Id: s.ID, RepositoryId: s.RepositoryID, Cron: s.Cron, Title: s.Title, Prompt: s.Prompt, AgentProfile: s.AgentProfile, Labels: s.Labels, Enabled: s.Enabled}, ExpectedRevision: s.Revision}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(webSchedule(value.Msg.Schedule), nil, nil)
}

func (b webBackend) DeleteSchedule(ctx context.Context, id string) webaccess.Outcome {
	_, err := b.cfg.API.Schedules.DeleteSchedule(ctx, webRequest(ctx, &v1.DeleteScheduleRequest{Id: id}))
	return outcome(nil, nil, err)
}

func (b webBackend) RunSchedule(ctx context.Context, id string) webaccess.Outcome {
	value, err := b.cfg.API.Schedules.RunSchedule(ctx, webRequest(ctx, &v1.RunScheduleRequest{Id: id}))
	if err != nil {
		return outcome(nil, nil, err)
	}
	return outcome(map[string]string{"item_id": value.Msg.ItemId}, nil, nil)
}
