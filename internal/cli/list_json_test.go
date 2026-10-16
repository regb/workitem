package cli

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/regb/workitem/internal/app"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/model"
)

func TestListJSONAddsDisplayGroupsWithoutChangingLifecycleSections(t *testing.T) {
	idle := app.WorkListItem{ID: "idle", Slug: "a-full-untruncated-slug", State: model.StateActive, AttentionRank: 2, Agent: &viewapp.AgentStatus{Bucket: "needs_attention"}}
	first := app.WorkListItem{ID: "first", State: model.StateActive, AttentionRank: 1, Agent: &viewapp.AgentStatus{Bucket: "needs_attention"}}
	busy := app.WorkListItem{ID: "busy", State: model.StateActive, Agent: &viewapp.AgentStatus{Bucket: "in_progress"}}
	broken := app.WorkListItem{ID: "broken", State: model.StateActive, Agent: &viewapp.AgentStatus{Bucket: "in_progress"}, Worktree: &viewapp.WorktreeStatus{Status: "problem"}}
	waiting := app.WorkListItem{ID: "waiting", State: model.StateActive, Waiting: true, Agent: &viewapp.AgentStatus{Bucket: "needs_attention"}}
	backlog := app.WorkListItem{ID: "backlog", State: model.StateBacklog}
	archived := app.WorkListItem{ID: "archived", State: model.StateClosed}
	res := app.WorkListResult{Sections: viewapp.Sections{Active: []app.WorkListItem{idle, busy, broken, first}, Waiting: []app.WorkListItem{waiting}, Backlog: []app.WorkListItem{backlog}, Closed: []app.WorkListItem{archived}}, Warnings: []string{"sample warning"}}
	agents := map[string]string{"idle": "idle", "busy": "busy", "broken": "busy", "waiting": "idle", "first": "idle"}
	for _, tt := range []struct {
		mode   string
		agents map[string]string
		keys   []string
	}{
		{"active", agents, []string{"needs_attention", "needs_fixing", "in_progress", "waiting", "backlog"}},
		{"all", agents, []string{"needs_attention", "needs_fixing", "in_progress", "waiting", "backlog", "closed"}},
		{"active", nil, []string{"active", "waiting", "backlog"}},
		{"archived", nil, []string{"active", "waiting", "backlog", "closed"}},
	} {
		result := workListJSON(res, tt.mode, tt.agents)
		if !reflect.DeepEqual(result.Sections, res.Sections) {
			t.Fatal("lifecycle sections changed")
		}
		sections := workDisplaySections(res, tt.mode, tt.agents)
		if len(result.DisplaySections) != len(sections) {
			t.Fatal(result.DisplaySections)
		}
		seen := map[string]bool{}
		for i, group := range result.DisplaySections {
			if group.Key != tt.keys[i] || group.Title != sections[i].Name {
				t.Fatalf("%+v", group)
			}
			for j, id := range group.ItemIDs {
				if id != sections[i].Items[j].ID || seen[id] {
					t.Fatalf("invalid display item %q", id)
				}
				seen[id] = true
			}
		}
		if tt.agents != nil && !reflect.DeepEqual(result.DisplaySections[0].ItemIDs, []string{"first", "idle"}) {
			t.Fatal("attention order lost")
		}
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip app.WorkListResult
		if err := json.Unmarshal(data, &roundTrip); err != nil || !reflect.DeepEqual(roundTrip, res) {
			t.Fatalf("old JSON reader changed: %v %+v", err, roundTrip)
		}
	}
}

func TestEmptyListJSONHasEmptyDisplayIDArrays(t *testing.T) {
	data, err := json.Marshal(workListJSON(app.WorkListResult{}, "active", nil))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		DisplaySections []struct {
			ItemIDs []string `json:"item_ids"`
		} `json:"display_sections"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	for _, group := range result.DisplaySections {
		if group.ItemIDs == nil {
			t.Fatal("empty ID list became null")
		}
	}
}
