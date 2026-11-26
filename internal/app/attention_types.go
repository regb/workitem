package app

import (
	"fmt"
	"time"

	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/model"
)

type AttentionActivity = viewapp.Activity

type DeferResult struct {
	WorkItemID string            `json:"work_item_id"`
	DeferredAt time.Time         `json:"deferred_at"`
	Activity   AttentionActivity `json:"activity"`
	Manifest   model.Manifest    `json:"manifest"`
	Warnings   []string          `json:"warnings"`
}

type ReadMarkerResult struct {
	WorkItemID string            `json:"work_item_id"`
	Unread     bool              `json:"unread"`
	MarkedAt   time.Time         `json:"marked_at"`
	Activity   AttentionActivity `json:"activity"`
	Warnings   []string          `json:"warnings"`
}

// NotNeedsAttentionError is an eligibility result, not an infrastructure
// failure: attention defer requires an active item.
type NotNeedsAttentionError struct {
	WorkItemID string
	State      string
}

func (e *NotNeedsAttentionError) Error() string {
	return fmt.Sprintf("work item %s is %s; attention defer requires an active item", e.WorkItemID, e.State)
}
