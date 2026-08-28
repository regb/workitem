package view

import (
	"testing"

	"github.com/regb/workitem/internal/model"
)

type manifestStore struct{ items []model.Manifest }

func (s manifestStore) ListManifests() ([]model.Manifest, []error) { return s.items, nil }
func TestDeepWorkCapacity(t *testing.T) {
	s := New(manifestStore{[]model.Manifest{{ID: "b", State: model.StateWaiting, DeepWork: true}, {ID: "a", State: model.StateWorking, DeepWork: true}, {ID: "ignored", State: model.StateBacklog, DeepWork: true}}})
	got := s.DeepWorkCapacity(3)
	if got.Active != 2 || got.Available != 1 || len(got.ItemIDs) != 2 || got.ItemIDs[0] != "a" || got.ItemIDs[1] != "b" {
		t.Fatalf("capacity=%+v", got)
	}
}
