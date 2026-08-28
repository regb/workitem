package attention

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type eventStore struct{ events []model.Event }

func (s *eventStore) AppendEvent(_ context.Context, _ string, event model.Event) error {
	s.events = append(s.events, event)
	return nil
}
func (s *eventStore) ReadEvents(string) ([]model.Event, error) { return s.events, nil }

func TestRecordDeferPreservesManifestAndEventContract(t *testing.T) {
	now := time.Date(2026, 4, 1, 2, 3, 4, 0, time.UTC)
	manifest := model.Manifest{ID: "item-1", State: model.StateWorking, UpdatedAt: now.Add(-time.Hour)}
	store := &eventStore{}
	service := New(store, func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return manifest, nil }, func() time.Time { return now })
	result, err := service.RecordDefer(context.Background(), contract.ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Manifest, manifest) {
		t.Fatalf("manifest mutated: got %+v want %+v", result.Manifest, manifest)
	}
	if result.Activity.LastDeferredAt == nil || !result.Activity.LastDeferredAt.Equal(now) {
		t.Fatalf("last deferred = %v", result.Activity.LastDeferredAt)
	}
	if len(store.events) != 1 || store.events[0].Type != "attention.deferred" || store.events[0].Actor != "user" {
		t.Fatalf("events = %+v", store.events)
	}
	if got, ok := store.events[0].Data["deferred_at"].(time.Time); !ok || !got.Equal(now) {
		t.Fatalf("event data = %#v", store.events[0].Data)
	}
	if _, exists := store.events[0].Data["state"]; exists {
		t.Fatalf("unexpected state in event data: %#v", store.events[0].Data)
	}
}

func TestRecordDeferRejectsNonWorkingWithoutWrite(t *testing.T) {
	store := &eventStore{}
	service := New(store, func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{ID: "item-1", State: model.StateWaiting}, nil
	}, time.Now)
	_, err := service.RecordDefer(context.Background(), contract.ResolveOptions{})
	var stateErr *StateError
	if !errors.As(err, &stateErr) || stateErr.WorkItemID != "item-1" || stateErr.State != model.StateWaiting {
		t.Fatalf("error = %v", err)
	}
	if len(store.events) != 0 {
		t.Fatalf("events = %+v", store.events)
	}
}
