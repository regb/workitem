package priority

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const DefaultStrategy = "recent-request"

type Candidate struct {
	ID              string     `json:"id"`
	DeepWork        bool       `json:"deep_work"`
	BaseOrder       int        `json:"base_order"`
	StateChangedAt  time.Time  `json:"state_changed_at"`
	LastRequestedAt *time.Time `json:"last_requested_at,omitempty"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	LastDeferredAt  *time.Time `json:"last_deferred_at,omitempty"`
}

type Strategy interface {
	Name() string
	Less(a, b Candidate) bool
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Strategy{}
)

func init() {
	Register(recentRequestStrategy{})
}

func Register(strategy Strategy) {
	if strategy == nil || strings.TrimSpace(strategy.Name()) == "" {
		panic("priority strategy must have a name")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[strategy.Name()] = strategy
}

func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func Rank(strategyName string, candidates []Candidate) ([]Candidate, error) {
	strategyName = strings.TrimSpace(strategyName)
	if strategyName == "" {
		strategyName = DefaultStrategy
	}
	registryMu.RLock()
	strategy, ok := registry[strategyName]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown attention priority strategy %q; available: %s", strategyName, strings.Join(Names(), ", "))
	}
	result := append([]Candidate{}, candidates...)
	sort.SliceStable(result, func(i, j int) bool { return strategy.Less(result[i], result[j]) })
	return result, nil
}

type recentRequestStrategy struct{}

func (recentRequestStrategy) Name() string { return DefaultStrategy }

func (recentRequestStrategy) Less(a, b Candidate) bool {
	aDeferred := effectivelyDeferred(a)
	bDeferred := effectivelyDeferred(b)
	if aDeferred != bDeferred {
		return !aDeferred
	}
	if aDeferred {
		if cmp := compareTimePointers(a.LastDeferredAt, b.LastDeferredAt, false); cmp != 0 {
			return cmp < 0
		}
	} else if cmp := compareTimePointers(a.LastRequestedAt, b.LastRequestedAt, true); cmp != 0 {
		return cmp < 0
	}
	if a.DeepWork != b.DeepWork {
		return a.DeepWork
	}
	if a.BaseOrder != b.BaseOrder {
		return a.BaseOrder < b.BaseOrder
	}
	return a.ID < b.ID
}

func effectivelyDeferred(candidate Candidate) bool {
	if candidate.LastDeferredAt == nil {
		return false
	}
	return candidate.LastRequestedAt == nil || candidate.LastDeferredAt.After(*candidate.LastRequestedAt)
}

func compareTimePointers(a, b *time.Time, descending bool) int {
	if a == nil && b == nil {
		return 0
	}
	if a != nil && b == nil {
		return -1
	}
	if a == nil && b != nil {
		return 1
	}
	if a.Equal(*b) {
		return 0
	}
	if descending {
		if a.After(*b) {
			return -1
		}
		return 1
	}
	if a.Before(*b) {
		return -1
	}
	return 1
}
