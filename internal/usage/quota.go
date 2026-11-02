package usage

import (
	"math"
	"time"
)

func Nonnegative(p *float64) bool {
	return p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0
}

// Native utilization is an adapter input only; exports contain remaining quota.
func NativeWindow(id string, percent *float64, reset, duration *int64) *Window {
	if !Nonnegative(percent) {
		return nil
	}
	remaining := math.Max(0, 100-*percent)
	w := &Window{ID: id, RemainingPercent: &remaining}
	if reset != nil && *reset > 0 && *reset < 253402300800 {
		t := time.Unix(*reset, 0).UTC()
		w.ResetsAt = &t
	}
	if duration != nil && *duration > 0 {
		w.DurationSeconds = duration
	}
	return w
}

// AgeObservation never rolls an elapsed window forward as zero utilization.
// Observation time is receipt time, not a provider-certified timestamp.
func AgeObservation(q Quota, now time.Time) Quota {
	stale := q.ObservedAt != nil && now.Sub(*q.ObservedAt) >= 5*time.Minute
	windows := []Window{}
	for _, w := range q.Windows {
		if w.ResetsAt != nil && !w.ResetsAt.After(now) {
			stale = true
			continue
		}
		windows = append(windows, w)
	}
	q.Windows = windows
	if stale && q.State == OK {
		q.State = Stale
		q.Detail = "Last native observation is old or a window has expired; no model request was sent."
	}
	return q
}
