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

type DeferResult struct {
	WorkItemID string         `json:"work_item_id"`
	DeferredAt time.Time      `json:"deferred_at"`
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
		Manifest:   m,
		Warnings:   []string{},
	}, nil
}
