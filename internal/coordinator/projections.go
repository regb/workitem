package coordinator

import (
	"strings"
	"time"
	"unicode"
)

const (
	AttentionActivityProjection = "attention_activity"
	RuntimeActivityProjection   = "runtime_activity"
)

type AttentionActivity struct {
	WorkItemID      string     `json:"work_item_id"`
	LastRequestedAt *time.Time `json:"last_requested_at,omitempty"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	LastDeferredAt  *time.Time `json:"last_deferred_at,omitempty"`
	ObservedAt      time.Time  `json:"observed_at"`
	Source          string     `json:"source"`
}

type RuntimeActivity struct {
	WorkItemID      string     `json:"work_item_id"`
	RuntimeID       string     `json:"runtime_id,omitempty"`
	RuntimeState    string     `json:"runtime_state,omitempty"`
	TurnState       string     `json:"turn_state,omitempty"`
	LastEventAt     *time.Time `json:"last_event_at,omitempty"`
	LastRequestedAt *time.Time `json:"last_requested_at,omitempty"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	ObservedAt      time.Time  `json:"observed_at"`
	Source          string     `json:"source"`
}

func compactIdentifier(value string, limit int) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}
