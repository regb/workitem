package app

import (
	"context"
	"time"
)

// Activity queries use the coordinator snapshot. Event replay belongs to
// projection rebuilds, not interactive commands or navigation.
func (a *App) attentionActivity(ctx context.Context, itemID string) (AttentionActivity, []string) {
	if a.AttentionActivityReader == nil {
		return AttentionActivity{}, []string{"attention activity projection is unavailable"}
	}
	activity, err := a.AttentionActivityReader(ctx, itemID)
	if err != nil {
		return AttentionActivity{}, []string{"could not read attention activity projection: " + err.Error()}
	}
	return activity, []string{}
}

func latestAttentionTime(current *time.Time, at time.Time) *time.Time {
	if current != nil && !current.Before(at) {
		return current
	}
	return timePointer(at)
}

func timePointer(value time.Time) *time.Time { copy := value; return &copy }
