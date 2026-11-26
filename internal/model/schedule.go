package model

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
)

const (
	// ScheduleGrace is how late a firing may still run: a daemon that was
	// briefly busy or asleep fires it; a longer outage skips it.
	ScheduleGrace     = 5 * time.Minute
	maxScheduleTitle  = 200
	maxSchedulePrompt = 64 << 10
)

// Schedule starts a new item in a repository on a cron schedule, as if its
// prompt had been sent from the dashboard. Runs are ordinary items.
type Schedule struct {
	ID           string `json:"id"`
	Revision     uint32 `json:"revision"`
	RepositoryID string `json:"repository_id"`
	Cron         string `json:"cron"`
	// TimeZone is the IANA time zone the cron expression and the text's
	// date and time are read in; empty is the daemon's local one.
	TimeZone     string `json:"time_zone,omitempty"`
	Title        string `json:"title,omitempty"`
	Prompt       string `json:"prompt"`
	AgentProfile string `json:"agent_profile"`
	// AgentSettings override the profile's settings, such as the model, for
	// each run's item.
	AgentSettings map[string]string `json:"agent_settings,omitempty"`
	Labels        []string          `json:"labels,omitempty"`
	Enabled       bool              `json:"enabled"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	// NextRunAt is the next firing of an enabled schedule.
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
	// LastRunAt, LastItemID and LastError describe the latest firing.
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	LastItemID string     `json:"last_item_id,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
}

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ParseCron parses a standard five-field cron expression, or a descriptor
// such as @daily. A schedule reads it in its time zone.
func ParseCron(expression string) (cron.Schedule, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return nil, errors.New("invalid cron expression: it is required")
	}
	if strings.HasPrefix(expression, "@every") || strings.HasPrefix(expression, "TZ=") || strings.HasPrefix(expression, "CRON_TZ=") {
		return nil, errors.New("invalid cron expression: use five fields or a descriptor such as @daily")
	}
	value, err := cronParser.Parse(expression)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	return value, nil
}

// Validate checks the fields a person sets.
func (s Schedule) Validate() error {
	if strings.TrimSpace(s.RepositoryID) == "" {
		return errors.New("a schedule requires a repository")
	}
	if strings.TrimSpace(s.AgentProfile) == "" {
		return errors.New("a schedule requires an agent profile")
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return errors.New("a schedule requires a prompt")
	}
	if len(s.Prompt) > maxSchedulePrompt || !utf8.ValidString(s.Prompt) {
		return fmt.Errorf("invalid prompt: at most %d bytes of text", maxSchedulePrompt)
	}
	if len(s.Title) > maxScheduleTitle || !utf8.ValidString(s.Title) || strings.ContainsAny(s.Title, "\r\n") {
		return fmt.Errorf("invalid title: one line of at most %d bytes", maxScheduleTitle)
	}
	if _, err := ParseCron(s.Cron); err != nil {
		return err
	}
	if _, err := s.Location(); err != nil {
		return err
	}
	if _, err := NormalizeLabels(s.Labels); err != nil {
		return err
	}
	return nil
}

// Next returns the first firing after t, or nil for a disabled or invalid
// schedule.
func (s Schedule) Next(t time.Time) *time.Time {
	if !s.Enabled {
		return nil
	}
	value, err := ParseCron(s.Cron)
	if err != nil {
		return nil
	}
	location, err := s.Location()
	if err != nil {
		return nil
	}
	next := value.Next(t.In(location))
	if next.IsZero() {
		return nil
	}
	next = next.UTC()
	return &next
}

// Due reports, at now, whether an enabled schedule has a firing to run: one
// no later than ScheduleGrace ago. A firing missed by longer is skipped. It
// also returns the firing's time and the next firing after now.
func (s Schedule) Due(now time.Time) (fire bool, due time.Time, next *time.Time) {
	if !s.Enabled || s.NextRunAt == nil || now.Before(*s.NextRunAt) {
		return false, time.Time{}, s.NextRunAt
	}
	return now.Sub(*s.NextRunAt) <= ScheduleGrace, *s.NextRunAt, s.Next(now)
}

// Location is the time zone the schedule is read in: its own, or the
// daemon's local one.
func (s Schedule) Location() (*time.Location, error) {
	if s.TimeZone == "" {
		return time.Local, nil
	}
	location, err := time.LoadLocation(s.TimeZone)
	if err != nil || s.TimeZone == "Local" {
		return nil, fmt.Errorf("invalid time zone %q: use an IANA name such as Europe/Paris, or none for the daemon's", s.TimeZone)
	}
	return location, nil
}

// RenderScheduleText fills a title or prompt template: {{date}}, {{time}},
// {{datetime}}, {{weekday}} in the given time zone, and {{repo}}.
// Other text, including unknown placeholders, is kept as written.
func RenderScheduleText(template string, at time.Time, location *time.Location, repository string) string {
	local := at.In(location)
	return strings.NewReplacer(
		"{{date}}", local.Format("2006-01-02"),
		"{{time}}", local.Format("15:04"),
		"{{datetime}}", local.Format("2006-01-02 15:04"),
		"{{weekday}}", local.Weekday().String(),
		"{{repo}}", repository,
	).Replace(template)
}
