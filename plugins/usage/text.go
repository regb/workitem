package usage

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/regb/workitem/internal/usage"
)

func writeUsageText(out io.Writer, quotas []usage.Quota, now time.Time) error {
	var b strings.Builder
	fmt.Fprintln(&b, "Remaining subscription quota.")
	if len(quotas) == 0 {
		fmt.Fprintln(&b, "No supported native clients found on PATH. Install Claude Code, Codex, or OpenCode v2 and sign in natively.")
	}
	results := usage.NewSnapshot(quotas).Results
	counts := map[string]int{}
	for _, q := range results {
		counts[q.Provider]++
	}
	for i, q := range results {
		if i > 0 {
			fmt.Fprintln(&b)
		}
		name := map[string]string{"claude": "Claude", "codex": "Codex", "opencode-go": "OpenCode Go"}[q.Provider]
		if name == "" {
			name = quotaLabel(q.Provider)
		}
		if counts[q.Provider] > 1 {
			name += " · " + quotaLabel(q.Account)
		}
		fmt.Fprintln(&b, name)
		width := 0
		for _, w := range q.Windows {
			if n := utf8.RuneCountInString(quotaWindowLabel(w)); n > width {
				width = n
			}
		}
		if len(q.Windows) == 0 {
			fmt.Fprintln(&b, "  Remaining quota unknown.")
		}
		for _, w := range q.Windows {
			bar, remaining := quotaBar(w.RemainingPercent)
			reset := "reset unknown"
			if w.ResetsAt != nil {
				if !w.ResetsAt.After(now) {
					reset = "reset elapsed"
				} else {
					reset = "resets in " + quotaDuration(w.ResetsAt.Sub(now))
				}
			}
			fmt.Fprintf(&b, "  %-*s  %s %8s left  %s\n", width, quotaWindowLabel(w), bar, remaining, reset)
		}
		if q.IncludedUsageAllowed != nil && !*q.IncludedUsageAllowed {
			fmt.Fprintln(&b, "  ! Included usage blocked")
		}
		if c := q.Credits; c != nil {
			if c.Unlimited != nil && *c.Unlimited {
				fmt.Fprintln(&b, "  Credits: unlimited")
			} else if usage.Nonnegative(c.Remaining) {
				fmt.Fprintf(&b, "  Credits: %g %s remaining\n", *c.Remaining, quotaLabel(c.Unit))
			}
		}
		if q.Stale {
			age := "observation time unknown"
			if q.ObservedAt != nil {
				if !now.After(*q.ObservedAt) {
					age = "observed just now"
				} else {
					age = "observed " + quotaDuration(now.Sub(*q.ObservedAt)) + " ago"
				}
			}
			fmt.Fprintf(&b, "  ! Stale, %s\n", age)
		} else if q.Error != nil {
			fmt.Fprintf(&b, "  ! %s\n", q.Error.Code)
		}
		if q.Error != nil {
			fmt.Fprintf(&b, "  %s\n", quotaLabel(q.Error.Message))
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}

// Remove terminal controls and multiline text from native display labels.
func quotaLabel(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}

func quotaWindowLabel(w usage.Window) string {
	label := w.ID
	switch w.ID {
	case "five_hour":
		label = "5-hour"
	case "seven_day", "weekly":
		label = "Weekly"
	case "monthly":
		label = "Monthly"
	default:
		if strings.HasPrefix(w.ID, "seven_day_") {
			label = "Weekly " + strings.ReplaceAll(strings.TrimPrefix(w.ID, "seven_day_"), "_", " ")
		} else if w.DurationSeconds != nil {
			period := ""
			switch *w.DurationSeconds {
			case 18000:
				period = "5-hour"
			case 604800:
				period = "Weekly"
			}
			if period != "" {
				label = period
				if w.ID != "codex/primary" && w.ID != "codex/secondary" {
					label += " · " + w.ID
				}
			}
		}
	}
	if w.Scope != nil {
		if w.Scope.Model != "" {
			label += " · " + w.Scope.Model
		}
		if w.Scope.Surface != "" {
			label += " · " + w.Scope.Surface
		}
	}
	return quotaLabel(label)
}

func quotaBar(percent *float64) (string, string) {
	if !usage.Nonnegative(percent) || *percent > 100 {
		return strings.Repeat(" ", 12), "unknown"
	}
	filled := int(math.Floor(*percent / 10))
	value := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", *percent), "0"), ".")
	if value == "100" && *percent < 100 {
		value = ">99.99"
	}
	if value == "0" && *percent > 0 {
		value = "<0.01"
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", 10-filled) + "]", value + "%"
}

func quotaDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	minutes := int64(math.Ceil(float64(d) / float64(time.Minute)))
	if minutes >= 1440 {
		days, hours := minutes/1440, minutes%1440/60
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if minutes >= 60 {
		hours, rest := minutes/60, minutes%60
		if rest == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh %dm", hours, rest)
	}
	return fmt.Sprintf("%dm", minutes)
}
