package app_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

type rejectArchivedStore struct{ app.Store }

func (s rejectArchivedStore) SaveManifest(ctx context.Context, m model.Manifest) error {
	if m.State == model.StateClosed {
		return fmt.Errorf("injected archive commit failure")
	}
	return s.Store.SaveManifest(ctx, m)
}
func TestArchiveReportsTerminalCleanupBeforeCommitFailure(t *testing.T) {
	st := store.New(t.TempDir())
	application := newTestApp(st, newFakeGit(t))
	application.NewID = func() (string, error) { return testutil.ID(t, 221), nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Archive partial", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.EnsureWorkItemWorkspace(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}); err != nil {
		t.Fatal(err)
	}
	mux := &fakeTmux{}
	application.Tmux = mux
	if _, err := application.EnsureTerminal(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}); err != nil {
		t.Fatal(err)
	}
	application.Store = rejectArchivedStore{st}
	result, err := application.Close(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, model.ResolutionCompleted, false)
	if err == nil || !strings.Contains(err.Error(), "workspace cleanup completed") || result.WorkItemID != created.Manifest.ID || result.State != model.StateBacklog {
		t.Fatalf("outcome %+v %v", result, err)
	}
	retained, err := st.LoadManifest(created.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.State != retained.State || result.Manifest.Checkout.Claim == nil {
		t.Fatalf("returned inferred cleanup instead of retained claim: %+v", result.Manifest)
	}
	if retained.State != model.StateBacklog || retained.Checkout.Claim == nil || len(mux.killed) != 1 || !containsString(result.Warnings, "closed terminal session "+retained.TerminalSessionName()) {
		t.Fatalf("partial was rolled back or archived %+v", retained)
	}
}
func TestArchiveTimeoutRetainsShutdownWarningAndLifecycle(t *testing.T) {
	st := store.New(testutil.ShortTempDir(t))
	application := newTestApp(st, newFakeGit(t))
	proc := &fakeProcess{alive: map[int]bool{4321: true}}
	application.Process = proc
	application.ArchiveRuntimeStopTimeout = 20 * time.Millisecond
	id := testutil.ID(t, 222)
	application.NewID = func() (string, error) { return id, nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Archive timeout", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := model.AgentRuntime{ID: "timeout-owner", WorkItemID: id, Mode: "headless", Capabilities: model.RuntimeCapabilities{IdleGuard: true}, HostPID: 4321, HostProcessGroup: 4321, HostStartTime: 4321}
	if err := st.SaveAgentRuntime(context.Background(), id, runtime); err != nil {
		t.Fatal(err)
	}
	control, err := agent.ListenControlSocket(filepath.Join(st.ItemDir(id), "agent", "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	go func() {
		req := <-control.Requests()
		if req.Command.Type != agent.CommandShutdown || !req.Command.RequireIdle {
			req.Respond(fmt.Errorf("shutdown did not require idle"))
			return
		}
		req.Respond(nil)
	}()
	result, err := application.Close(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, model.ResolutionCompleted, false)
	if err == nil || !strings.Contains(err.Error(), "did not exit") || !containsString(result.Warnings, "driver accepted runtime shutdown") || result.State != model.StateBacklog {
		t.Fatalf("timeout outcome %+v %v", result, err)
	}
	retained, _ := st.LoadManifest(id)
	if retained.State != model.StateBacklog {
		t.Fatal("timeout applied archive")
	}
}
