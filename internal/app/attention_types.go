package app

import (
	"fmt"
	"time"

	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/model"
)

const attentionDeferredEvent = "attention.deferred"

type AttentionActivity = viewapp.Activity

type AttentionCandidate struct {
	Item     WorkListItem      `json:"item"`
	Activity AttentionActivity `json:"activity"`
	Rank     int               `json:"rank"`
}

type AttentionQueueOptions struct {
	WorkListOptions
	ResolveOptions
}

type AttentionQueueResult struct {
	Strategy   string               `json:"strategy"`
	Candidates []AttentionCandidate `json:"candidates"`
	Warnings   []string             `json:"warnings"`
}

type DeferResult struct {
	WorkItemID string            `json:"work_item_id"`
	DeferredAt time.Time         `json:"deferred_at"`
	Activity   AttentionActivity `json:"activity"`
	Manifest   model.Manifest    `json:"manifest"`
	Warnings   []string          `json:"warnings"`
}

type WorkItemActivityResult struct {
	WorkItemID string            `json:"work_item_id"`
	Activity   AttentionActivity `json:"activity"`
	Warnings   []string          `json:"warnings"`
}

// NotNeedsAttentionError is an eligibility result, not an infrastructure
// failure. Adapter porcelain may safely skip an optional defer when this
// error is observed.
type NotNeedsAttentionError struct {
	WorkItemID string
	State      string
	Agent      string
	Worktree   string
}

func (e *NotNeedsAttentionError) Error() string {
	if e.State != model.StateWorking {
		return fmt.Sprintf("work item %s is %s; attention defer requires working state", e.WorkItemID, e.State)
	}
	return fmt.Sprintf("work item %s is not in NEEDS ATTENTION (agent=%s, worktree=%s)", e.WorkItemID, e.Agent, e.Worktree)
}
