package cli

import (
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// The page folds and colors sections by these names, as wi list shows them.
func TestWebSectionsUseTheListNamesAndSeparateProblems(t *testing.T) {
	problem := &v1.ItemObservation{AgentStatus: v1.AgentStatus_AGENT_STATUS_PROBLEM}
	broken := &v1.ItemObservation{WorktreeStatus: v1.WorktreeStatus_WORKTREE_STATUS_PROBLEM}
	for _, test := range []struct {
		row  *v1.ItemSummary
		want string
	}{
		{&v1.ItemSummary{AttentionGroup: v1.AttentionGroup_ATTENTION_GROUP_NEEDS_ATTENTION}, "NEEDS ATTENTION"},
		{&v1.ItemSummary{AttentionGroup: v1.AttentionGroup_ATTENTION_GROUP_NEEDS_ATTENTION, Observation: problem}, "NEEDS FIXING"},
		{&v1.ItemSummary{AttentionGroup: v1.AttentionGroup_ATTENTION_GROUP_WORKING, Observation: broken}, "NEEDS FIXING"},
		{&v1.ItemSummary{AttentionGroup: v1.AttentionGroup_ATTENTION_GROUP_WORKING}, "IN PROGRESS"},
		{&v1.ItemSummary{AttentionGroup: v1.AttentionGroup_ATTENTION_GROUP_WAITING, Observation: problem}, "WAITING"},
		{&v1.ItemSummary{AttentionGroup: v1.AttentionGroup_ATTENTION_GROUP_BACKLOG}, "BACKLOG"},
		{&v1.ItemSummary{AttentionGroup: v1.AttentionGroup_ATTENTION_GROUP_CLOSED}, "CLOSED"},
	} {
		if got := webSection(test.row); got != test.want {
			t.Errorf("webSection(%v) = %q, want %q", test.row, got, test.want)
		}
	}
}
