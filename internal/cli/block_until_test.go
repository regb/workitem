package cli

import (
	"testing"
	"time"
)

func TestParseBlockUntilAcceptsDurationsDaysAndLocalDates(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	for input, want := range map[string]time.Time{
		"90m":              now.Add(90 * time.Minute),
		"4h":               now.Add(4 * time.Hour),
		"2d":               now.Add(48 * time.Hour),
		"1.5d":             now.Add(36 * time.Hour),
		"2026-10-20":       time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local),
		"2026-10-20T09:00": time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local),
		"tomorrow":         time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local),
		"Monday":           time.Date(2026, 10, 19, 9, 0, 0, 0, time.Local),
		"thursday":         time.Date(2026, 10, 22, 9, 0, 0, 0, time.Local),
		"friday":           time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local),
	} {
		got, err := parseBlockUntil(now, input)
		if err != nil || !got.Equal(want) {
			t.Fatalf("%q = %s, %v; want %s", input, got, err, want)
		}
	}
	for _, input := range []string{"", "-4h", "0d", "2026-10-01", "someday"} {
		if _, err := parseBlockUntil(now, input); err == nil {
			t.Fatalf("%q accepted", input)
		}
	}
}

func TestWeekdayEarlierTodayMeansNextWeek(t *testing.T) {
	morning := time.Date(2026, 10, 15, 8, 0, 0, 0, time.Local)
	if got, _ := parseBlockUntil(morning, "thursday"); !got.Equal(time.Date(2026, 10, 15, 9, 0, 0, 0, time.Local)) {
		t.Fatalf("thursday before 09:00 = %s", got)
	}
}
