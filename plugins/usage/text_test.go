package usage

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/usage"
)

func TestUsageCompactText(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	percent, full := 74.5, 100.
	five, week := int64(18000), int64(604800)
	reset, weeklyReset := now.Add(130*time.Minute), now.Add(4*24*time.Hour+3*time.Hour)
	quotas := []usage.Quota{
		{Account: usage.Account{Provider: "claude", ID: "opaque"}, State: usage.OK, ObservedAt: &now, Windows: []usage.Window{{ID: "five_hour", RemainingPercent: &percent, ResetsAt: &reset}}},
		{Account: usage.Account{Provider: "codex"}, State: usage.OK, Windows: []usage.Window{{ID: "codex/primary", RemainingPercent: &full, DurationSeconds: &five, ResetsAt: &reset}, {ID: "codex/secondary", RemainingPercent: &percent, DurationSeconds: &week, ResetsAt: &weeklyReset}}},
		{Account: usage.Account{Provider: "opencode-go"}, State: usage.OK, Windows: []usage.Window{{ID: "monthly", RemainingPercent: &percent}}},
	}
	var out bytes.Buffer
	if err := writeUsageText(&out, quotas, now); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Claude\n", "Codex\n", "OpenCode Go\n", "5-hour", "Weekly", "Monthly", "[███████░░░]", "[██████████]", "74.5% left", "100% left", "resets in 2h 10m", "resets in 4d 3h", "reset unknown"} {
		if !strings.Contains(out.String(), required) {
			t.Fatalf("missing %q:\n%s", required, out.String())
		}
	}
	for _, hidden := range []string{"hidden-source", "opaque", "authentication", "Checked", "Next native", "\x1b"} {
		if strings.Contains(out.String(), hidden) {
			t.Fatal(out.String())
		}
	}
}
func TestUsageBarsUnknownAndEdges(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), -1, 101} {
		bar, number := quotaBar(&value)
		if strings.Contains(bar, "[") || number != "unknown" {
			t.Fatal(bar, number)
		}
	}
	if _, number := quotaBar(nil); number != "unknown" {
		t.Fatal(number)
	}
	for _, tc := range []struct {
		value float64
		want  string
	}{{0, "0%"}, {100, "100%"}, {99.98, "99.98%"}, {99.9999, ">99.99%"}, {0.00001, "<0.01%"}} {
		_, number := quotaBar(&tc.value)
		if number != tc.want {
			t.Fatal(tc.value, number)
		}
	}
}
func TestUsageResetCountdownBoundaries(t *testing.T) {
	for _, tc := range []struct {
		duration time.Duration
		want     string
	}{
		{time.Second, "<1m"}, {time.Minute, "1m"}, {61 * time.Second, "2m"}, {time.Hour, "1h"}, {time.Hour + time.Minute, "1h 1m"}, {24 * time.Hour, "1d"}, {49 * time.Hour, "2d 1h"},
	} {
		if got := quotaDuration(tc.duration); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	now := time.Now()
	value := 25.
	var out bytes.Buffer
	if err := writeUsageText(&out, []usage.Quota{{State: usage.OK, Windows: []usage.Window{{ID: "five_hour", RemainingPercent: &value, ResetsAt: &now}}}}, now); err != nil || !strings.Contains(out.String(), "reset elapsed") {
		t.Fatal(out.String(), err)
	}
}
func TestUsageTextScopesStaleErrorsAndMultipleAccounts(t *testing.T) {
	now := time.Now()
	observed := now.Add(-2 * time.Hour)
	blocked := false
	var out bytes.Buffer
	quotas := []usage.Quota{
		{Account: usage.Account{Provider: "claude", ID: "one"}, State: usage.Stale, ObservedAt: &observed, Detail: "Refresh failed", IncludedUsageAllowed: &blocked, Windows: []usage.Window{{ID: "seven_day_sonnet", Scope: &usage.WindowScope{Model: "Fable", Surface: "CLI"}}}},
		{Account: usage.Account{Provider: "claude", ID: "two"}, State: usage.Unavailable, Detail: "Sign in natively"},
	}
	if err := writeUsageText(&out, quotas, now); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"Claude · one", "Claude · two", "Weekly sonnet · Fable · CLI", "unknown left", "Stale, observed 2h ago", "Included usage blocked", "Sign in natively"} {
		if !strings.Contains(out.String(), value) {
			t.Fatal(value, out.String())
		}
	}
	if strings.Contains(out.String(), "[░") {
		t.Fatal("unknown quota got an exhausted bar")
	}
	if got := quotaWindowLabel(usage.Window{ID: "other-bucket/primary", DurationSeconds: func() *int64 { v := int64(18000); return &v }()}); !strings.Contains(got, "other-bucket/primary") {
		t.Fatal("lost bucket identity")
	}
	if strings.Contains(quotaLabel("native\x1b[31m\nlabel"), "\x1b") {
		t.Fatal("terminal control leaked")
	}
}

type failedUsageWriter struct{}

func (failedUsageWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func TestUsageTextWriterFailure(t *testing.T) {
	if err := writeUsageText(failedUsageWriter{}, nil, time.Now()); err == nil {
		t.Fatal("write error lost")
	}
}
