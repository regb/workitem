package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/regb/workitem/internal/model"
)

// Schedule starts a new item in a repository on a cron schedule, as if its
// prompt had been sent from the dashboard.
type Schedule struct {
	ID           string     `json:"id,omitempty"`
	Revision     uint32     `json:"revision,omitempty"`
	RepositoryID string     `json:"repository_id"`
	Cron         string     `json:"cron"`
	Title        string     `json:"title,omitempty"`
	Prompt       string     `json:"prompt"`
	AgentProfile string     `json:"agent_profile"`
	Labels       []string   `json:"labels"`
	Enabled      bool       `json:"enabled"`
	NextRunAt    *time.Time `json:"next_run_at,omitempty"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	LastItemID   string     `json:"last_item_id,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

// ScheduleBackend is the part of the Wi composition that manages schedules.
type ScheduleBackend interface {
	Schedules(context.Context) ([]Schedule, error)
	// PutSchedule creates a schedule (no ID) or replaces one's settings,
	// fenced on its revision; the result is the saved schedule.
	PutSchedule(context.Context, Schedule) Outcome
	DeleteSchedule(ctx context.Context, id string) Outcome
	// RunSchedule fires a schedule now; the result names the item it
	// started.
	RunSchedule(ctx context.Context, id string) Outcome
}

// validateSchedule checks the shape of an edit; the daemon checks the rest,
// such as the cron expression and that the profile exists.
func validateSchedule(s *Schedule) error {
	switch {
	case s.ID != "" && !model.ValidID(s.ID):
		return errors.New("invalid schedule")
	case !model.ValidID(s.RepositoryID):
		return errors.New("a repository is required")
	case strings.TrimSpace(s.Cron) == "":
		return errors.New("a cron expression is required")
	case strings.TrimSpace(s.AgentProfile) == "":
		return errors.New("an agent profile is required")
	case strings.TrimSpace(s.Prompt) == "":
		return errors.New("a prompt is required")
	}
	return nil
}

func (s *Server) scheduleRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/schedules", read(s.backend.Schedules))
	m.HandleFunc("POST /api/schedules", func(w http.ResponseWriter, r *http.Request) {
		var value Schedule
		if decodeValid(w, r, &value, 80<<10, validateSchedule) {
			outcome(w, s.backend.PutSchedule(r.Context(), value))
		}
	})
	byID := func(act func(context.Context, string) Outcome) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			if !model.ValidID(id) {
				badRequest(w, errors.New("valid schedule required"))
				return
			}
			outcome(w, act(r.Context(), id))
		}
	}
	m.HandleFunc("POST /api/schedules/{id}/delete", byID(s.backend.DeleteSchedule))
	m.HandleFunc("POST /api/schedules/{id}/run", byID(s.backend.RunSchedule))
}
