package daemonapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// scheduleTick is how often the daemon looks for due schedules. Firings run
// up to a tick late, well within model.ScheduleGrace.
const scheduleTick = 30 * time.Second

func scheduleMessage(value model.Schedule) *v1.Schedule {
	out := &v1.Schedule{Id: value.ID, Revision: value.Revision, RepositoryId: value.RepositoryID, Cron: value.Cron, Title: value.Title, Prompt: value.Prompt, AgentProfile: value.AgentProfile, AgentSettings: value.AgentSettings, Labels: append([]string{}, value.Labels...), Enabled: value.Enabled, CreatedAt: timestamppb.New(value.CreatedAt), UpdatedAt: timestamppb.New(value.UpdatedAt), LastItemId: value.LastItemID, LastError: value.LastError}
	if value.NextRunAt != nil {
		out.NextRunAt = timestamppb.New(*value.NextRunAt)
	}
	if value.LastRunAt != nil {
		out.LastRunAt = timestamppb.New(*value.LastRunAt)
	}
	return out
}

func scheduleError(err error) error {
	if errors.Is(err, coordinator.ErrScheduleChanged) {
		return connect.NewError(connect.CodeAborted, err)
	}
	return rpcError(err)
}

func (s *Server) ListSchedules(ctx context.Context, req *connect.Request[v1.ListSchedulesRequest]) (*connect.Response[v1.ListSchedulesResponse], error) {
	values, err := s.Core.Database().ListSchedules()
	if err != nil {
		return nil, rpcError(err)
	}
	out := &v1.ListSchedulesResponse{Schedules: []*v1.Schedule{}}
	for _, value := range values {
		out.Schedules = append(out.Schedules, scheduleMessage(value))
	}
	return connect.NewResponse(out), nil
}

func (s *Server) PutSchedule(ctx context.Context, req *connect.Request[v1.PutScheduleRequest]) (*connect.Response[v1.PutScheduleResponse], error) {
	r := req.Msg.Schedule
	if r == nil {
		return nil, invalid("schedule is required")
	}
	value := model.Schedule{ID: r.Id, RepositoryID: r.RepositoryId, Cron: r.Cron, Title: r.Title, Prompt: r.Prompt, AgentProfile: r.AgentProfile, AgentSettings: r.AgentSettings, Labels: r.Labels, Enabled: r.Enabled}
	if err := value.Validate(); err != nil {
		return nil, invalid(err.Error())
	}
	repo, err := s.repo(ctx, value.RepositoryID)
	if err != nil {
		return nil, rpcError(err)
	}
	value.RepositoryID = repo.Id
	profiles, err := s.Local.AgentProfiles(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	if _, ok := profiles.Find(value.AgentProfile); !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no agent profile %s", value.AgentProfile))
	}
	// A setting each run's item would refuse fails now, not at the run.
	if value.AgentSettings, err = app.NormalizeAgentSettings(value.AgentSettings); err != nil {
		return nil, invalid(err.Error())
	}
	saved, err := s.Core.Database().PutSchedule(value, req.Msg.ExpectedRevision, time.Now().UTC())
	if err != nil {
		return nil, scheduleError(err)
	}
	return connect.NewResponse(&v1.PutScheduleResponse{Schedule: scheduleMessage(saved)}), nil
}

func (s *Server) DeleteSchedule(ctx context.Context, req *connect.Request[v1.DeleteScheduleRequest]) (*connect.Response[v1.DeleteScheduleResponse], error) {
	if err := s.Core.Database().DeleteSchedule(req.Msg.Id); err != nil {
		return nil, scheduleError(err)
	}
	return connect.NewResponse(&v1.DeleteScheduleResponse{}), nil
}

func (s *Server) RunSchedule(ctx context.Context, req *connect.Request[v1.RunScheduleRequest]) (*connect.Response[v1.RunScheduleResponse], error) {
	value, found, err := s.Core.Database().Schedule(req.Msg.Id)
	if err != nil {
		return nil, rpcError(err)
	}
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("schedule %s not found", req.Msg.Id))
	}
	id, err := model.NewID()
	if err != nil {
		return nil, rpcError(err)
	}
	itemID, err := s.fireSchedule(ctx, value, time.Now().UTC(), "schedule:"+value.ID+":run:"+id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.RunScheduleResponse{ItemId: itemID}), nil
}

// fireSchedule starts the schedule's item as the dashboard starts one: the
// prompt as its description and first message, a generated title unless the
// schedule sets one, and a headless agent. key makes a repeated firing
// create no second item. The outcome is recorded on the schedule.
func (s *Server) fireSchedule(ctx context.Context, value model.Schedule, at time.Time, key string) (string, error) {
	repoName := value.RepositoryID
	if repo, err := s.repo(ctx, value.RepositoryID); err == nil {
		repoName = repo.Name
	}
	title := model.RenderScheduleText(value.Title, at, repoName)
	prompt := model.RenderScheduleText(value.Prompt, at, repoName)
	request := &v1.CreateAndStartItemRequest{
		Item:  &v1.CreateItemRequest{RepositoryId: value.RepositoryID, Title: title, GenerateTitle: title == "", Description: prompt, Labels: value.Labels, AgentProfile: value.AgentProfile, AgentSettings: value.AgentSettings, RequestId: key},
		Start: &v1.StartOptions{Prompt: prompt, Mode: v1.AgentMode_AGENT_MODE_HEADLESS, ClientMessageId: key},
	}
	itemID, failure := "", ""
	created, err := s.CreateAndStartItem(ctx, connect.NewRequest(request))
	if created != nil && created.Msg.Item != nil {
		itemID = created.Msg.Item.Id
	}
	if err != nil {
		failure = err.Error()
		if itemID == "" {
			itemID = partialItem(err)
		}
	}
	if recordErr := s.Core.Database().RecordScheduleRun(value.ID, at, itemID, failure); recordErr != nil {
		fmt.Fprintf(os.Stderr, "wi: record schedule %s run: %v\n", value.ID, recordErr)
	}
	return itemID, err
}

// runDueSchedules fires every enabled schedule due at now. Each firing is
// taken once: the schedule moves to its next run before the item is
// created, so a crash in between skips that run rather than repeating it.
func (s *Server) runDueSchedules(now time.Time) {
	values, err := s.Core.Database().ListSchedules()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wi: list schedules: %v\n", err)
		return
	}
	for _, value := range values {
		if value.NextRunAt == nil || now.Before(*value.NextRunAt) {
			continue
		}
		fire, due, _ := value.Due(now)
		if _, taken, err := s.Core.Database().AdvanceSchedule(value.ID, due, now); err != nil || !taken {
			if err != nil {
				fmt.Fprintf(os.Stderr, "wi: advance schedule %s: %v\n", value.ID, err)
			}
			continue
		}
		if !fire {
			continue
		}
		if !s.startCallback() {
			return
		}
		go func(value model.Schedule, due time.Time) {
			defer s.callbacks.Done()
			ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
			defer cancel()
			_, _ = s.fireSchedule(ctx, value, due, "schedule:"+value.ID+":"+strconv.FormatInt(due.Unix(), 10))
		}(value, due)
	}
}

// startCallback registers background work, unless the server is closing.
func (s *Server) startCallback() bool {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	if s.callbacksClosed {
		return false
	}
	s.callbacks.Add(1)
	return true
}

func (s *Server) scheduleLoop() {
	defer s.callbacks.Done()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			s.runDueSchedules(time.Now().UTC())
			timer.Reset(scheduleTick)
		}
	}
}

// partialItem is the item a partial outcome names, if any.
func partialItem(err error) string {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return ""
	}
	for _, detail := range ce.Details() {
		value, e := detail.Value()
		if outcome, ok := value.(*v1.PartialOutcome); e == nil && ok {
			return outcome.ItemId
		}
	}
	return ""
}
