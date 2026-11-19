package model

import (
	"strings"
	"testing"
	"time"
)

func TestScheduleValidation(t *testing.T) {
	valid := Schedule{RepositoryID: "r", Cron: "0 9 * * 1-5", Prompt: "Triage CI", AgentProfile: "claude"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Schedule){
		"no repository": func(s *Schedule) { s.RepositoryID = "" },
		"no agent":      func(s *Schedule) { s.AgentProfile = " " },
		"no prompt":     func(s *Schedule) { s.Prompt = "\n" },
		"no cron":       func(s *Schedule) { s.Cron = "" },
		"bad cron":      func(s *Schedule) { s.Cron = "61 * * * *" },
		"seconds field": func(s *Schedule) { s.Cron = "0 0 9 * * *" },
		"@every":        func(s *Schedule) { s.Cron = "@every 1m" },
		"time zone":     func(s *Schedule) { s.Cron = "CRON_TZ=UTC 0 9 * * *" },
		"long title":    func(s *Schedule) { s.Title = strings.Repeat("x", 201) },
		"title lines":   func(s *Schedule) { s.Title = "a\nb" },
		"bad label":     func(s *Schedule) { s.Labels = []string{"semi;colon"} },
	} {
		s := valid
		change(&s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, expression := range []string{"@daily", "*/15 * * * *", "0 9 * * MON-FRI"} {
		s := valid
		s.Cron = expression
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", expression, err)
		}
	}
}

func TestScheduleFiresWithinGraceAndSkipsLongerMisses(t *testing.T) {
	due := time.Date(2026, 11, 19, 9, 0, 0, 0, time.Local)
	s := Schedule{Cron: "0 9 * * *", Enabled: true, NextRunAt: &due}
	if fire, _, _ := s.Due(due.Add(-time.Second)); fire {
		t.Fatal("fired early")
	}
	fire, at, next := s.Due(due.Add(4 * time.Minute))
	if !fire || !at.Equal(due) || next == nil || !next.Equal(due.AddDate(0, 0, 1).UTC()) {
		t.Fatalf("on time: fire=%v at=%v next=%v", fire, at, next)
	}
	fire, _, next = s.Due(due.Add(3 * time.Hour))
	if fire || next == nil || !next.Equal(due.AddDate(0, 0, 1).UTC()) {
		t.Fatalf("missed by hours: fire=%v next=%v", fire, next)
	}
	s.Enabled = false
	if fire, _, _ := s.Due(due); fire || s.Next(due) != nil {
		t.Fatal("disabled schedule fires")
	}
}

func TestScheduleTextRendersKnownPlaceholders(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 5, 0, 0, time.Local)
	got := RenderScheduleText("{{repo}} {{date}} {{time}} {{weekday}} {{datetime}} {{other}}", at, "workitem")
	if got != "workitem 2026-11-19 09:05 Thursday 2026-11-19 09:05 {{other}}" {
		t.Fatal(got)
	}
}
