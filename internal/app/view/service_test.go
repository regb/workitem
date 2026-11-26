package view

import (
	"slices"
	"testing"

	"github.com/regb/workitem/internal/model"
)

type manifestStore struct{ items []model.Manifest }

func (s manifestStore) ListManifests() ([]model.Manifest, []error) { return s.items, nil }
func TestDeepWorkCapacity(t *testing.T) {
	s := New(manifestStore{[]model.Manifest{{ID: "b", State: model.StateActive, DeepWork: true, Blockers: []model.Blocker{{ID: "b1", Kind: model.BlockerExternal, Event: "review"}}}, {ID: "a", State: model.StateActive, DeepWork: true}, {ID: "ignored", State: model.StateBacklog, DeepWork: true}}})
	got := s.DeepWorkCapacity(3)
	if got.Active != 2 || got.Available != 1 || len(got.ItemIDs) != 2 || got.ItemIDs[0] != "a" || got.ItemIDs[1] != "b" {
		t.Fatalf("capacity=%+v", got)
	}
}

func TestWorkListFiltersByLabelRules(t *testing.T) {
	s := New(manifestStore{[]model.Manifest{
		{ID: "matching", State: model.StateBacklog, Labels: []string{"jira", "team"}},
		{ID: "excluded", State: model.StateBacklog, Labels: []string{"jira", "personal", "team"}},
		{ID: "missing", State: model.StateBacklog, Labels: []string{"team"}},
	}})
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{"positive and negative rules combine", Options{LabelRules: map[string]bool{"team": true, "jira": true, "personal": false}}, []string{"matching"}},
		{"a label filter adds a positive rule", Options{Label: "Personal"}, []string{"excluded"}},
		{"no rules keep everything", Options{}, []string{"excluded", "matching", "missing"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := []string{}
			for _, item := range s.WorkList(tc.opts, 1, 1).Sections.Backlog {
				got = append(got, item.ID)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("backlog = %v, want %v", got, tc.want)
			}
		})
	}
}
