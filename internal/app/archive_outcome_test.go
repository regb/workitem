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
	created := createItem(t, application, app.NewWorkItemOptions{Title: "Archive partial"})
	if _, err := application.EnsureWorkItemWorkspace(context.Background(), app.ResolveOptions{Selector: created.ID}); err != nil {
		t.Fatal(err)
	}
	// The item has a terminal, which closing must close.
	mux := &fakeTmux{sessions: map[string]bool{created.TerminalSessionName(): true}}
	application.Tmux = mux
	if err := st.SaveTerminalRuntime(context.Background(), created.ID, model.TerminalRuntime{}); err != nil {
		t.Fatal(err)
	}
	application.Store = rejectArchivedStore{st}
	result, err := application.Close(context.Background(), app.ResolveOptions{Selector: created.ID}, app.CloseOptions{Resolution: model.ResolutionCompleted})
	if err == nil || !strings.Contains(err.Error(), "workspace cleanup completed") || result.WorkItemID != created.ID || result.State != model.StateBacklog {
		t.Fatalf("outcome %+v %v", result, err)
	}
	retained, err := st.LoadManifest(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.State != retained.State || result.Manifest.Checkout.Claim != nil {
		t.Fatalf("returned state differs from durable workspace release: %+v", result.Manifest)
	}
	if retained.State != model.StateBacklog || retained.Checkout.Claim != nil || len(mux.killed) != 1 || !containsString(result.Warnings, "closed terminal session "+retained.TerminalSessionName()) {
		t.Fatalf("partial was rolled back or archived %+v", retained)
	}
}

type failWorkspaceReleaseStore struct {
	app.Store
	commit bool
}

func (s failWorkspaceReleaseStore) SaveManifest(ctx context.Context, m model.Manifest) error {
	before, err := s.Store.LoadManifest(m.ID)
	if err != nil {
		return err
	}
	if before.Checkout.Claim != nil && m.Checkout.Claim == nil {
		if s.commit {
			if err := s.Store.SaveManifest(ctx, m); err != nil {
				return err
			}
		}
		return fmt.Errorf("injected workspace release failure")
	}
	return s.Store.SaveManifest(ctx, m)
}
func TestArchiveReleaseFailureReportsCanonicalClaimAndDoesNotClose(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed=%t", committed), func(t *testing.T) {
			st := store.New(t.TempDir())
			a := newTestApp(st, newFakeGit(t))
			ctx := context.Background()
			created := createItem(t, a, app.NewWorkItemOptions{Title: "Release partial"})
			opts := app.ResolveOptions{Selector: created.ID}
			workspace, err := a.EnsureWorkItemWorkspace(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			a.Store = failWorkspaceReleaseStore{Store: st, commit: committed}
			result, err := a.Close(ctx, opts, app.CloseOptions{Resolution: model.ResolutionCompleted})
			if err == nil || !strings.Contains(err.Error(), "injected workspace release failure") {
				t.Fatal("release failure was hidden", err)
			}
			retained, err := st.LoadManifest(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if retained.State != created.State || result.State != retained.State || result.Manifest.State != retained.State || result.Changed {
				t.Fatal("release failure closed the item")
			}
			if (retained.Checkout.Claim == nil) != committed || (result.Manifest.Checkout.Claim == nil) != committed {
				t.Fatal("partial outcome did not report canonical claim")
			}
			if retained.Checkout.ClaimGeneration != workspace.Manifest.Checkout.ClaimGeneration || retained.Checkout.Specification.Branch != workspace.Manifest.Checkout.Specification.Branch {
				t.Fatal("release failure changed checkout identity")
			}
		})
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
	created := createItem(t, application, app.NewWorkItemOptions{Title: "Archive timeout"})
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
	result, err := application.Close(context.Background(), app.ResolveOptions{Selector: created.ID}, app.CloseOptions{Resolution: model.ResolutionCompleted})
	if err == nil || !strings.Contains(err.Error(), "did not exit") || !containsString(result.Warnings, "driver accepted runtime shutdown") || result.State != model.StateBacklog {
		t.Fatalf("timeout outcome %+v %v", result, err)
	}
	retained, _ := st.LoadManifest(id)
	if retained.State != model.StateBacklog {
		t.Fatal("timeout applied archive")
	}
}

func TestShelveStopsAnIdleAgentBeforeReleasingTheCheckout(t *testing.T) {
	st := store.New(testutil.ShortTempDir(t))
	application := newTestApp(st, newFakeGit(t))
	application.Process = &fakeProcess{alive: map[int]bool{4321: true}}
	application.ArchiveRuntimeStopTimeout = 20 * time.Millisecond
	id := testutil.ID(t, 224)
	application.NewID = func() (string, error) { return id, nil }
	createItem(t, application, app.NewWorkItemOptions{Title: "Shelve running"})
	activateItem(t, application, id)
	runtime := model.AgentRuntime{ID: "shelve-owner", WorkItemID: id, Mode: "headless", Capabilities: model.RuntimeCapabilities{IdleGuard: true}, HostPID: 4321, HostProcessGroup: 4321, HostStartTime: 4321}
	if err := st.SaveAgentRuntime(context.Background(), id, runtime); err != nil {
		t.Fatal(err)
	}
	control, err := agent.ListenControlSocket(filepath.Join(st.ItemDir(id), "agent", "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	requested := make(chan agent.ControlCommand, 1)
	go func() {
		req := <-control.Requests()
		requested <- req.Command
		req.Respond(nil)
	}()
	// The fake process never exits, so shelving stops at the runtime step and
	// leaves the item active rather than releasing a checkout under an agent.
	result, err := application.Shelve(context.Background(), app.ResolveOptions{Selector: id}, false)
	if err == nil || !strings.Contains(err.Error(), "did not exit") || result.State != model.StateActive {
		t.Fatalf("shelve outcome %+v %v", result, err)
	}
	if command := <-requested; command.Type != agent.CommandShutdown || !command.RequireIdle {
		t.Fatalf("shelve sent %+v, want an idle-only shutdown", command)
	}
	retained, _ := st.LoadManifest(id)
	if retained.State != model.StateActive {
		t.Fatal("shelve applied while the agent was still running")
	}
}

func TestCloseCommitsUncommittedChangesOnlyWhenAsked(t *testing.T) {
	st := store.New(t.TempDir())
	git := newFakeGit(t)
	application := newTestApp(st, git)
	application.NewID = func() (string, error) { return testutil.ID(t, 224), nil }
	created := createItem(t, application, app.NewWorkItemOptions{Title: "Dirty close"})
	if _, err := application.EnsureWorkItemWorkspace(context.Background(), app.ResolveOptions{Selector: created.ID}); err != nil {
		t.Fatal(err)
	}
	git.status = " M file.go\n"
	opts := app.ResolveOptions{Selector: created.ID}
	if _, err := application.Close(context.Background(), opts, app.CloseOptions{Resolution: model.ResolutionCompleted}); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty close was not refused: %v", err)
	}
	if len(git.commits) != 0 {
		t.Fatalf("committed without being asked: %v", git.commits)
	}
	result, err := application.Close(context.Background(), opts, app.CloseOptions{Resolution: model.ResolutionCompleted, CommitChanges: true})
	if err != nil || result.State != model.StateClosed {
		t.Fatalf("close %+v %v", result, err)
	}
	if len(git.commits) != 1 || !containsString(result.Warnings, "committed uncommitted changes to "+created.Checkout.Specification.Branch+" as c0ffee012345") {
		t.Fatalf("commits %v warnings %v", git.commits, result.Warnings)
	}
	events, err := st.ReadEvents(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		found = found || event.Type == "workspace.changes_committed"
	}
	if !found {
		t.Fatal("no workspace.changes_committed event")
	}
}

func TestArchiveStopsRuntimeBeforeArchiving(t *testing.T) {
	st := store.New(testutil.ShortTempDir(t))
	fg := newFakeGit(t)
	proc := &fakeProcess{alive: map[int]bool{4321: true}}
	application := newTestApp(st, fg)
	application.Process = proc
	application.Clock = fixedClock{testutil.Time()}
	itemID := testutil.ID(t, 77)
	application.NewID = func() (string, error) { return itemID, nil }
	created := createItem(t, application, app.NewWorkItemOptions{Title: "Archive Runtime"})
	runtime := model.AgentRuntime{ID: "runtime-archive", WorkItemID: itemID, Mode: string(agent.ModeHeadless), HostPID: 4321, HostProcessGroup: 4321, HostStartTime: 4321}
	if err := st.SaveAgentRuntime(context.Background(), itemID, runtime); err != nil {
		t.Fatal(err)
	}
	server, err := agent.ListenControlSocket(filepath.Join(st.ItemDir(itemID), "agent", "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		request := <-server.Requests()
		if request.Command.Type != agent.CommandShutdown || request.Command.ProtocolVersion != agent.RuntimeProtocolVersion || request.Command.RuntimeID != runtime.ID || request.Command.WorkItemID != itemID {
			request.Respond(fmt.Errorf("unexpected shutdown command: %+v", request.Command))
			return
		}
		request.Respond(nil)
		proc.setAlive(4321, false)
	}()
	archived, err := application.Close(context.Background(), app.ResolveOptions{Selector: created.ID}, app.CloseOptions{Resolution: model.ResolutionCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if archived.State != model.StateClosed || !containsString(archived.Warnings, "requested agent runtime shutdown") {
		t.Fatalf("archived = %+v", archived)
	}
}

type forbiddenArchiveTmux struct {
	fakeTmux
	t *testing.T
}

func (f *forbiddenArchiveTmux) HasSession(context.Context, string) (bool, error) {
	f.t.Error("headless-only archive inspected tmux")
	return false, nil
}
func TestHeadlessOnlyArchiveDoesNotInspectTmux(t *testing.T) {
	st := store.New(t.TempDir())
	application := newTestApp(st, newFakeGit(t))
	application.Tmux = &forbiddenArchiveTmux{t: t}
	id := testutil.ID(t, 223)
	application.NewID = func() (string, error) { return id, nil }
	created := createItem(t, application, app.NewWorkItemOptions{Title: "No terminal"})
	if _, err := application.EnsureWorkItemWorkspace(context.Background(), app.ResolveOptions{Selector: id}); err != nil {
		t.Fatal(err)
	}
	runtime := model.AgentRuntime{ID: "stopped-headless", WorkItemID: id, Mode: "headless"}
	if err := st.SaveAgentRuntime(context.Background(), id, runtime); err != nil {
		t.Fatal(err)
	}
	result, err := application.Close(context.Background(), app.ResolveOptions{Selector: created.ID, Env: map[string]string{"TMUX": "caller-pane"}}, app.CloseOptions{Resolution: model.ResolutionCompleted})
	if err != nil || result.State != model.StateClosed || result.Manifest.Checkout.Claim != nil {
		t.Fatalf("archive %+v %v", result, err)
	}
}
