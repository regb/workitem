package item

import (
	"context"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type BlockerResult struct {
	WorkItemID string          `json:"work_item_id"`
	Added      []model.Blocker `json:"added,omitempty"`
	Cleared    []model.Blocker `json:"cleared,omitempty"`
	Changed    bool            `json:"changed"`
	Waiting    bool            `json:"waiting"`
	Manifest   model.Manifest  `json:"manifest"`
	Warnings   []string        `json:"warnings"`
}

// BlockerCommandStore commits validated blocker intent and its events atomically.
// There is deliberately no read/modify/save fallback for this operation.
type BlockerCommandStore interface {
	AddBlockers(context.Context, string, []model.Blocker, time.Time) (BlockerResult, error)
}

func (s *Service) AddBlockers(ctx context.Context, opts contract.ResolveOptions, blockers []model.Blocker) (BlockerResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return BlockerResult{}, err
	}
	commands, ok := s.store.(BlockerCommandStore)
	if !ok {
		return BlockerResult{}, fmt.Errorf("blocker changes require the coordinator command store")
	}
	return commands.AddBlockers(ctx, m.ID, blockers, s.now())
}

// BlockerMutationResult renders an already committed mutation. It does not
// validate or modify policy; the coordinator owns those decisions.
func BlockerMutationResult(before, after model.Manifest, changed bool, now time.Time) BlockerResult {
	result := BlockerResult{WorkItemID: after.ID, Changed: changed, Waiting: after.Waiting(now), Manifest: after, Warnings: []string{}}
	present := map[string]bool{}
	for _, b := range after.Blockers {
		present[b.ID] = true
	}
	previous := map[string]bool{}
	for _, b := range before.Blockers {
		previous[b.ID] = true
		if !present[b.ID] {
			result.Cleared = append(result.Cleared, b)
		}
	}
	for _, b := range after.Blockers {
		if !previous[b.ID] {
			result.Added = append(result.Added, b)
		}
	}
	return result
}
