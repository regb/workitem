package attention

import (
	"context"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type Store interface {
	AppendEvent(context.Context, string, model.Event) error
	ReadEvents(string) ([]model.Event, error)
}
type Resolver func(context.Context, contract.ResolveOptions) (model.Manifest, error)
type Service struct {
	store   Store
	resolve Resolver
	now     func() time.Time
}

func New(st Store, r Resolver, now func() time.Time) *Service {
	return &Service{store: st, resolve: r, now: now}
}

type Activity struct {
	LastRequestedAt *time.Time `json:"last_requested_at,omitempty"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	LastDeferredAt  *time.Time `json:"last_deferred_at,omitempty"`
	LastReadAt      *time.Time `json:"last_read_at,omitempty"`
	LastUnreadAt    *time.Time `json:"last_unread_at,omitempty"`
}
type DeferResult struct {
	WorkItemID string         `json:"work_item_id"`
	DeferredAt time.Time      `json:"deferred_at"`
	Activity   Activity       `json:"activity"`
	Manifest   model.Manifest `json:"manifest"`
	Warnings   []string       `json:"warnings"`
}

type StateError struct {
	WorkItemID string
	State      string
}

func (e *StateError) Error() string {
	return fmt.Sprintf("work item %s is %s; attention defer requires an active item", e.WorkItemID, e.State)
}

func (s *Service) FoldActivity(itemID string, lastRequestedAt, lastCompletedAt *time.Time) (Activity, []string) {
	activity := Activity{LastRequestedAt: lastRequestedAt, LastCompletedAt: lastCompletedAt}
	warnings := []string{}
	events, err := s.store.ReadEvents(itemID)
	if err != nil {
		return activity, append(warnings, "could not read attention events: "+err.Error())
	}
	later := func(current **time.Time, value time.Time) {
		if !value.IsZero() && (*current == nil || value.After(**current)) {
			copy := value
			*current = &copy
		}
	}
	for _, event := range events {
		switch event.Type {
		case "attention.deferred":
			later(&activity.LastDeferredAt, event.Time)
		case "attention.read":
			later(&activity.LastReadAt, event.Time)
		case "attention.unread":
			later(&activity.LastUnreadAt, event.Time)
		}
	}
	return activity, warnings
}

// RecordRead marks every agent response so far as read. Reading never moves
// an unblocked item in the queue; it only returns a blocked item to review.
func (s *Service) RecordRead(ctx context.Context, opts contract.ResolveOptions) (model.Manifest, time.Time, error) {
	return s.recordMarker(ctx, opts, "attention.read")
}

// RecordUnread restores the unread signal, for example after a response was
// opened by accident.
func (s *Service) RecordUnread(ctx context.Context, opts contract.ResolveOptions) (model.Manifest, time.Time, error) {
	return s.recordMarker(ctx, opts, "attention.unread")
}

func (s *Service) recordMarker(ctx context.Context, opts contract.ResolveOptions, eventType string) (model.Manifest, time.Time, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return model.Manifest{}, time.Time{}, err
	}
	now := s.now()
	if err := s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, eventType, "user", map[string]any{"at": now})); err != nil {
		return model.Manifest{}, time.Time{}, err
	}
	return m, now, nil
}

func (s *Service) RecordDefer(ctx context.Context, opts contract.ResolveOptions) (DeferResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return DeferResult{}, err
	}
	if m.State != model.StateActive {
		return DeferResult{}, &StateError{WorkItemID: m.ID, State: m.State}
	}
	now := s.now()
	if err := s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "attention.deferred", "user", map[string]any{"deferred_at": now})); err != nil {
		return DeferResult{}, err
	}
	return DeferResult{
		WorkItemID: m.ID,
		DeferredAt: now,
		Activity:   Activity{LastDeferredAt: &now},
		Manifest:   m,
		Warnings:   []string{},
	}, nil
}
