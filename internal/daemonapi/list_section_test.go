package daemonapi

import (
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestListSectionFollowsAttentionThenProblems(t *testing.T) {
	problem := &v1.ItemObservation{AgentStatus: v1.AgentStatus_AGENT_STATUS_PROBLEM}
	checkout := &v1.ItemObservation{WorktreeStatus: v1.WorktreeStatus_WORKTREE_STATUS_PROBLEM}
	for _, test := range []struct {
		group v1.AttentionGroup
		o     *v1.ItemObservation
		want  v1.ListSection
	}{
		{v1.AttentionGroup_ATTENTION_GROUP_CLOSED, problem, v1.ListSection_LIST_SECTION_CLOSED},
		{v1.AttentionGroup_ATTENTION_GROUP_BACKLOG, problem, v1.ListSection_LIST_SECTION_BACKLOG},
		// A usage-limited agent is a problem, but its item waits.
		{v1.AttentionGroup_ATTENTION_GROUP_WAITING, problem, v1.ListSection_LIST_SECTION_WAITING},
		{v1.AttentionGroup_ATTENTION_GROUP_WORKING, checkout, v1.ListSection_LIST_SECTION_NEEDS_FIXING},
		{v1.AttentionGroup_ATTENTION_GROUP_NEEDS_ATTENTION, problem, v1.ListSection_LIST_SECTION_NEEDS_FIXING},
		{v1.AttentionGroup_ATTENTION_GROUP_WORKING, &v1.ItemObservation{}, v1.ListSection_LIST_SECTION_IN_PROGRESS},
		{v1.AttentionGroup_ATTENTION_GROUP_NEEDS_ATTENTION, nil, v1.ListSection_LIST_SECTION_NEEDS_ATTENTION},
	} {
		if got := listSection(&v1.ItemSummary{AttentionGroup: test.group, Observation: test.o}); got != test.want {
			t.Errorf("%v %v: got %v, want %v", test.group, test.o, got, test.want)
		}
	}
}
