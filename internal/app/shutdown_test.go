package app_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestShutdownAllForceStopsTrackedRuntimeAndClosesOwnedTmuxSessions(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{sessions: map[string]bool{}, sessionEnvs: map[string]map[string]string{}}
	fp := &fakeProcess{alive: map[int]bool{321: true}}
	application := newTestApp(st, fg)
	application.Tmux = ft
	application.Process = fp
	application.Clock = fixedClock{testutil.Time()}
	application.ShutdownRuntimeStopTimeout = 20 * time.Millisecond
	application.NewID = func() (string, error) { return testutil.ID(t, 90), nil }
	created := createItem(t, application, app.NewWorkItemOptions{Title: "Shutdown all"})
	itemID := created.ID
	runtime := model.AgentRuntime{ID: "tui-shutdown", WorkItemID: itemID, Mode: "tui", State: model.AgentRuntimeRunning, HostPID: 321, HostProcessGroup: 321, HostStartTime: 321, StartedAt: testutil.Time(), UpdatedAt: testutil.Time()}
	if err := st.SaveAgentRuntime(context.Background(), itemID, runtime); err != nil {
		t.Fatal(err)
	}
	canonicalSession := created.TerminalSessionName()
	canonicalDir := st.ItemDir(itemID)
	ft.sessions[canonicalSession] = true
	ft.sessionEnvs[canonicalSession] = map[string]string{
		"WI_ID":                     itemID,
		"WI_DIR":                    canonicalDir,
		"WI_AGENT_CONVERSATION_DIR": filepath.Join(canonicalDir, "sessions"),
	}

	orphanID := testutil.ID(t, 91)
	orphanSession := model.TerminalSessionName(orphanID)
	orphanDir := st.ItemDir(orphanID)
	ft.sessions[orphanSession] = true
	ft.sessionEnvs[orphanSession] = map[string]string{
		"WI_ID":                     orphanID,
		"WI_DIR":                    orphanDir,
		"WI_AGENT_CONVERSATION_DIR": filepath.Join(orphanDir, "sessions"),
	}
	spoofedSession := model.TerminalSessionName(testutil.ID(t, 92))
	ft.sessions[spoofedSession] = true
	ft.sessionEnvs[spoofedSession] = map[string]string{"WI_ID": testutil.ID(t, 92)}

	result, err := application.ShutdownAll(context.Background(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete() {
		t.Fatalf("shutdown failures = %+v", result.Failures)
	}
	if fp.Alive(321) {
		t.Fatal("tracked runtime process remained alive")
	}
	if len(result.Items) != 1 || !result.Items[0].RuntimeStopped || !result.Items[0].TerminalClosed {
		t.Fatalf("item result = %+v", result.Items)
	}
	if ft.sessions[canonicalSession] || ft.sessions[orphanSession] {
		t.Fatalf("owned sessions remained: %+v", ft.sessions)
	}
	if !ft.sessions[spoofedSession] {
		t.Fatal("session without the full wi ownership environment was killed")
	}
	if len(result.OrphanedTerminalsClosed) != 1 || result.OrphanedTerminalsClosed[0] != orphanSession {
		t.Fatalf("orphaned terminals = %+v", result.OrphanedTerminalsClosed)
	}
}

func TestShutdownAllLeavesTerminalWhenRuntimeCannotBeStopped(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{sessions: map[string]bool{}}
	fp := &fakeProcess{alive: map[int]bool{654: true}}
	application := newTestApp(st, fg)
	application.Tmux = ft
	application.Process = fp
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return testutil.ID(t, 93), nil }
	created := createItem(t, application, app.NewWorkItemOptions{Title: "Safe shutdown refusal"})
	runtime := model.AgentRuntime{ID: "tui-refuse", WorkItemID: created.ID, Mode: "tui", State: model.AgentRuntimeRunning, HostPID: 654, HostProcessGroup: 654, HostStartTime: 654, StartedAt: testutil.Time(), UpdatedAt: testutil.Time()}
	if err := st.SaveAgentRuntime(context.Background(), created.ID, runtime); err != nil {
		t.Fatal(err)
	}
	session := created.TerminalSessionName()
	ft.sessions[session] = true

	result, err := application.ShutdownAll(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete() || len(result.Failures) == 0 {
		t.Fatalf("expected shutdown failures, got %+v", result)
	}
	if !fp.Alive(654) {
		t.Fatal("non-forced shutdown signaled a runtime after socket refusal")
	}
	if !ft.sessions[session] {
		t.Fatal("terminal was closed while its runtime remained active")
	}
}
