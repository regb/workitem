package app_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	processpkg "github.com/regb/workitem/internal/process"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

// Agent status is derived from the primary conversation, the runtime's
// processes and the worktree.
func TestAgentStatusDerivesFromConversationProcessAndWorktree(t *testing.T) {
	now := testutil.Time()
	stamp := now.Add(-time.Hour).Format(time.RFC3339Nano)
	piProcess := &fakeProcess{alive: map[int]bool{123: true, 456: true}, descendant: processpkg.Info{PID: 456, Command: "pi", Cmdline: []string{"pi"}}, found: true}
	tests := []struct {
		name       string
		process    *fakeProcess
		gitStatus  string
		staleAfter time.Duration
		entries    []map[string]any
		check      func(t *testing.T, status app.AgentStatusResult)
	}{
		{
			name:       "busy with an open request and a live backend",
			process:    piProcess,
			staleAfter: 10 * time.Minute,
			entries:    []map[string]any{piMessage(now.Add(-time.Minute), "user", []map[string]any{{"type": "text", "text": "please work"}})},
			check: func(t *testing.T, status app.AgentStatusResult) {
				if status.Status != "busy" || status.Conversation.InferredTurnState != "incomplete" || !status.Process.Online || status.Process.BackendPID != 456 {
					t.Fatalf("status = %+v", status)
				}
			},
		},
		{
			name:       "session metadata is not turn activity",
			process:    piProcess,
			staleAfter: 10 * time.Minute,
			entries: []map[string]any{
				{"type": "session", "timestamp": stamp},
				{"type": "model_change", "timestamp": stamp},
				{"type": "thinking_level_change", "timestamp": stamp},
			},
			check: func(t *testing.T, status app.AgentStatusResult) {
				if status.Status != "idle" || status.Conversation.InferredTurnState != "idle" || status.Conversation.LastTurnActivity != nil || status.Conversation.LastEvent == nil || status.Conversation.LastEvent.Type != "thinking_level_change" || status.Conversation.LastActivityAgeSeconds != 0 {
					t.Fatalf("status = %+v", status)
				}
			},
		},
		{
			name:      "idle after a final answer, with changes in the worktree",
			process:   &fakeProcess{alive: map[int]bool{123: true}},
			gitStatus: " M file.go",
			entries: []map[string]any{
				piMessage(now.Add(-2*time.Minute), "user", []map[string]any{{"type": "text", "text": "please implement"}}),
				piMessage(now.Add(-time.Minute), "assistant", []map[string]any{{"type": "text", "text": "Implemented and tested."}}),
			},
			check: func(t *testing.T, status app.AgentStatusResult) {
				if status.Status != "idle" || status.Conversation.InferredTurnState != "idle" || status.Worktree == nil || status.Worktree.Status != "changed" || !status.Worktree.HasChanges {
					t.Fatalf("status = %+v", status)
				}
			},
		},
		{
			name:       "problem when a turn is incomplete and nothing runs",
			process:    &fakeProcess{alive: map[int]bool{}},
			staleAfter: 10 * time.Minute,
			entries:    []map[string]any{piMessage(now.Add(-time.Hour), "assistant", []map[string]any{{"type": "toolCall"}})},
			check: func(t *testing.T, status app.AgentStatusResult) {
				if status.Status != "problem" {
					t.Fatalf("status = %+v", status)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New(t.TempDir())
			fg := newFakeGit(t)
			fg.status = tc.gitStatus
			application := newTestApp(st, fg)
			application.Tmux = &fakeTmux{}
			application.Process = tc.process
			application.Clock = fixedClock{now}
			started, _ := startItem(t, application, app.NewWorkItemOptions{Title: "Status"}, agent.ModeTUI)
			appendPiJSONL(t, filepath.Join(st.ItemDir(started.ID), started.PrimaryConversation.NativeLocator()), tc.entries...)
			status, err := application.AgentStatus(context.Background(), app.AgentStatusOptions{ResolveOptions: app.ResolveOptions{Selector: started.ID}, StaleAfter: tc.staleAfter})
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, status)
		})
	}
}
