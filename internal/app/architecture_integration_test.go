package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	piadapter "github.com/regb/workitem/internal/pi"
	processpkg "github.com/regb/workitem/internal/process"
	"github.com/regb/workitem/internal/runtimepath"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
)

type fakeGit struct {
	info          gitpkg.RepositoryInfo
	head          string
	status        string
	currentBranch string
	branches      map[string]bool
	worktrees     map[string]string
}

func newFakeGit(t *testing.T) *fakeGit {
	t.Helper()
	return &fakeGit{info: repoInfo(t), branches: map[string]bool{}, worktrees: map[string]string{}}
}

func (f *fakeGit) DetectRepository(ctx context.Context, dir, revision string) (gitpkg.RepositoryInfo, error) {
	return f.info, nil
}
func (f *fakeGit) DefaultBranch(context.Context, string) (string, error) { return "main", nil }
func (f *fakeGit) RepositoryHome(context.Context, string) (model.RepositoryHomeInfo, error) {
	return model.RepositoryHomeInfo{Path: f.info.Repository.RootAtCreation, Branch: "main"}, nil
}
func (f *fakeGit) Head(ctx context.Context, dir string) (string, error) {
	if f.head != "" {
		return f.head, nil
	}
	return f.info.Commit, nil
}
func (f *fakeGit) StatusPorcelain(ctx context.Context, dir string) (string, error) {
	return f.status, nil
}
func (f *fakeGit) CurrentBranch(ctx context.Context, dir string) (string, error) {
	if f.currentBranch != "" {
		return f.currentBranch, nil
	}
	if f.worktrees != nil {
		if branch := f.worktrees[dir]; branch != "" {
			return branch, nil
		}
	}
	return "main", nil
}
func (f *fakeGit) BranchExists(ctx context.Context, repoRoot, branch string) (bool, error) {
	return f.branches[branch], nil
}
func (f *fakeGit) WorktreeAdd(ctx context.Context, opts gitpkg.WorktreeAddOptions) error {
	if err := os.MkdirAll(opts.Path, 0o700); err != nil {
		return err
	}
	if opts.NewBranch {
		f.branches[opts.Branch] = true
	}
	branch := opts.Branch
	if branch == "" {
		branch = opts.StartPoint
	}
	f.worktrees[opts.Path] = branch
	return nil
}
func (f *fakeGit) Switch(ctx context.Context, dir, branch, startPoint string, create bool) error {
	if create {
		f.branches[branch] = true
	}
	if f.worktrees == nil {
		f.worktrees = map[string]string{}
	}
	f.worktrees[dir] = branch
	return nil
}
func (f *fakeGit) WorktreeRemove(ctx context.Context, repoRoot, path string, force bool) error {
	delete(f.worktrees, path)
	return os.RemoveAll(path)
}
func (f *fakeGit) DeleteBranch(ctx context.Context, repoRoot, branch string, force bool) error {
	delete(f.branches, branch)
	return nil
}

type fakeTmux struct {
	current     string
	env         map[string]string
	sessionEnvs map[string]map[string]string
	sessions    map[string]bool
	ensured     []tmuxpkg.SessionSpec
	launched    []tmuxpkg.LaunchSpec
	attach      []string
	killed      []string
	killedAsync []string
	ensureErr   error
	launchErr   error
	killErr     error
	panePath    string
	panePaths   map[string]string
	onLaunch    func(tmuxpkg.LaunchSpec) error
}

func (f *fakeTmux) HasSession(ctx context.Context, name string) (bool, error) {
	if f.sessions != nil {
		return f.sessions[name], nil
	}
	return false, nil
}
func (f *fakeTmux) EnsureSession(ctx context.Context, spec tmuxpkg.SessionSpec) (bool, error) {
	f.ensured = append(f.ensured, spec)
	if f.sessions == nil {
		f.sessions = map[string]bool{}
	}
	f.sessions[spec.Name] = true
	if f.ensureErr != nil {
		return true, f.ensureErr
	}
	return true, nil
}
func (f *fakeTmux) LaunchCommand(ctx context.Context, spec tmuxpkg.LaunchSpec) error {
	f.launched = append(f.launched, spec)
	window := spec.WindowName
	if spec.ReuseAgentWindow || window == "" {
		window = "agent"
	}
	if f.panePaths == nil {
		f.panePaths = map[string]string{}
	}
	f.panePaths[spec.SessionName+":"+window] = spec.CWD
	if f.onLaunch != nil {
		if err := f.onLaunch(spec); err != nil {
			return err
		}
	}
	return f.launchErr
}
func (f *fakeTmux) PaneInfo(ctx context.Context, target string) (tmuxpkg.PaneInfo, error) {
	path := f.panePath
	if path == "" {
		path = f.panePaths[target]
	}
	if path == "" {
		path = "/tmp"
	}
	return tmuxpkg.PaneInfo{SessionName: "wi-test", WindowName: "agent", PaneID: "%1", PaneIndex: "0", PanePID: 123, Command: "wi", CurrentPath: path}, nil
}
func (f *fakeTmux) AttachOrSwitch(ctx context.Context, name string, inTmux bool) error {
	f.attach = append(f.attach, name)
	return nil
}
func (f *fakeTmux) KillSession(ctx context.Context, name string) error {
	f.killed = append(f.killed, name)
	if f.killErr != nil {
		return f.killErr
	}
	if f.sessions != nil {
		delete(f.sessions, name)
	}
	return nil
}
func (f *fakeTmux) KillSessionAsync(ctx context.Context, name string) error {
	f.killedAsync = append(f.killedAsync, name)
	if f.killErr != nil {
		return f.killErr
	}
	return nil
}
func (f *fakeTmux) CurrentSession(ctx context.Context) (string, error) { return f.current, nil }
func (f *fakeTmux) ListSessions(ctx context.Context) ([]string, error) {
	sessions := []string{}
	for session, present := range f.sessions {
		if present {
			sessions = append(sessions, session)
		}
	}
	return sessions, nil
}
func (f *fakeTmux) SessionEnvironment(ctx context.Context, name string) (map[string]string, error) {
	if f.sessionEnvs != nil && f.sessionEnvs[name] != nil {
		return f.sessionEnvs[name], nil
	}
	return f.env, nil
}
func (f *fakeTmux) GlobalEnvironment(ctx context.Context) (map[string]string, error) {
	return f.env, nil
}

type fakeProcess struct {
	mu          sync.RWMutex
	alive       map[int]bool
	descendant  processpkg.Info
	found       bool
	findErr     error
	findNeedles []string
}

func (f *fakeProcess) Alive(pid int) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.alive != nil && f.alive[pid]
}
func (f *fakeProcess) Info(pid int) (processpkg.Info, error) {
	if !f.Alive(pid) {
		return processpkg.Info{}, os.ErrNotExist
	}
	return processpkg.Info{PID: pid, PGRP: pid, StartTime: uint64(pid), State: "S"}, nil
}
func (f *fakeProcess) setAlive(pid int, alive bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.alive == nil {
		f.alive = map[int]bool{}
	}
	f.alive[pid] = alive
}
func (f *fakeProcess) TerminateGroup(pid int) error {
	f.setAlive(pid, false)
	return nil
}

func (f *fakeProcess) FindDescendant(rootPID int, needles []string) (processpkg.Info, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findNeedles = append([]string(nil), needles...)
	return f.descendant, f.found, f.findErr
}

type fakePi struct {
	sessionPath string
	cwd         string
	env         map[string]string
	mode        agent.Mode
	logPath     string
	controlPath string
}

func (f *fakePi) ExecMode(ctx context.Context, spec piadapter.ExecSpec) error {
	f.sessionPath = spec.SessionPath
	f.cwd = spec.CWD
	f.env = spec.Env
	f.mode = spec.Mode
	f.logPath = spec.LogPath
	f.controlPath = spec.ControlSocketPath
	return nil
}
func (f *fakePi) SessionCWD(path string) (string, error) {
	return piadapter.New("pi").SessionCWD(path)
}
func (f *fakePi) ForkSession(ctx context.Context, sourcePath, targetPath, targetCWD string) error {
	return piadapter.New("pi").ForkSession(ctx, sourcePath, targetPath, targetCWD)
}

type fakeRuntimeLauncher struct {
	spec    agent.LaunchSpec
	pid     int
	err     error
	onStart func(agent.LaunchSpec) error
}

func (f *fakeRuntimeLauncher) Start(spec agent.LaunchSpec) (int, error) {
	f.spec = spec
	if f.onStart != nil {
		if err := f.onStart(spec); err != nil {
			return 0, err
		}
	}
	return f.pid, f.err
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func TestShutdownAllForceStopsTrackedRuntimeAndClosesOwnedTmuxSessions(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{sessions: map[string]bool{}, sessionEnvs: map[string]map[string]string{}}
	fp := &fakeProcess{alive: map[int]bool{321: true}}
	application := app.New(st, fg)
	application.Tmux = ft
	application.Process = fp
	application.Clock = fixedClock{testutil.Time()}
	application.ShutdownRuntimeStopTimeout = 20 * time.Millisecond
	application.NewID = func() (string, error) { return testutil.ID(t, 90), nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Shutdown all", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	itemID := created.Manifest.ID
	runtime := model.AgentRuntime{ID: "tui-shutdown", WorkItemID: itemID, Mode: "tui", State: model.AgentRuntimeRunning, HostPID: 321, HostProcessGroup: 321, HostStartTime: 321, StartedAt: testutil.Time(), UpdatedAt: testutil.Time()}
	if err := st.SaveAgentRuntime(context.Background(), itemID, runtime); err != nil {
		t.Fatal(err)
	}
	canonicalSession := created.Manifest.TerminalSessionName()
	canonicalDir := st.ItemDir(itemID)
	ft.sessions[canonicalSession] = true
	ft.sessionEnvs[canonicalSession] = map[string]string{
		"WI_ID":                       itemID,
		"WI_DIR":                      canonicalDir,
		"PI_CODING_AGENT_SESSION_DIR": filepath.Join(canonicalDir, "sessions", "pi"),
	}

	orphanID := testutil.ID(t, 91)
	orphanSession := model.TerminalSessionName(orphanID)
	orphanDir := st.ItemDir(orphanID)
	ft.sessions[orphanSession] = true
	ft.sessionEnvs[orphanSession] = map[string]string{
		"WI_ID":                       orphanID,
		"WI_DIR":                      orphanDir,
		"PI_CODING_AGENT_SESSION_DIR": filepath.Join(orphanDir, "sessions", "pi"),
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
	application := app.New(st, fg)
	application.Tmux = ft
	application.Process = fp
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return testutil.ID(t, 93), nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Safe shutdown refusal", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := model.AgentRuntime{ID: "tui-refuse", WorkItemID: created.Manifest.ID, Mode: "tui", State: model.AgentRuntimeRunning, HostPID: 654, HostProcessGroup: 654, HostStartTime: 654, StartedAt: testutil.Time(), UpdatedAt: testutil.Time()}
	if err := st.SaveAgentRuntime(context.Background(), created.Manifest.ID, runtime); err != nil {
		t.Fatal(err)
	}
	session := created.Manifest.TerminalSessionName()
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

func appendPiJSONL(t *testing.T, path string, entries ...map[string]any) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
}

func piMessage(ts time.Time, role string, content []map[string]any) map[string]any {
	return map[string]any{"type": "message", "timestamp": ts.Format(time.RFC3339Nano), "message": map[string]any{"role": role, "content": content}}
}

func TestStateTransitionsAndCapacity(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	application.DeepWorkConfig.MaxActive = 1
	ids := []string{testutil.ID(t, 40), testutil.ID(t, 41), testutil.ID(t, 47), testutil.ID(t, 48)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	first, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "First", CWD: t.TempDir(), DeepWork: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Second", CWD: t.TempDir(), DeepWork: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.State != model.StateBacklog {
		t.Fatalf("new item state = %+v", first.Manifest)
	}
	startRes, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: first.Manifest.ID}, false, false)
	started := startRes.Transition
	if err != nil {
		t.Fatal(err)
	}
	if started.State != model.StateWorking || !started.Changed {
		t.Fatalf("started = %+v", started)
	}
	if _, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: second.Manifest.ID}, false, false); err == nil {
		t.Fatal("expected deep work capacity refusal")
	}
	waiting, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: first.Manifest.ID}, model.StateWaiting, false)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.State != model.StateWaiting {
		t.Fatalf("waiting = %+v", waiting)
	}
	resumed, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: first.Manifest.ID}, model.StateWorking, false)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != model.StateWorking {
		t.Fatalf("resumed = %+v", resumed)
	}
	shelved, err := application.Shelve(context.Background(), app.ResolveOptions{Selector: first.Manifest.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if shelved.State != model.StateBacklog {
		t.Fatalf("shelved = %+v", shelved)
	}
	forceRes, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: second.Manifest.ID}, true, false)
	forced := forceRes.Transition
	if err != nil {
		t.Fatal(err)
	}
	if forced.State != model.StateWorking {
		t.Fatalf("forced = %+v", forced)
	}
}

func TestResumeWorkItemAttachesWorkspace(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := app.New(st, fg)
	application.Tmux = ft
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 69), testutil.ID(t, 70)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Resume Attach", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, model.StateWaiting, false); err != nil {
		t.Fatal(err)
	}
	ft.attach = nil
	res, err := application.ResumeWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Transition.State != model.StateWorking || res.Workspace.Terminal.Session == "" || !res.Workspace.Terminal.Attached {
		t.Fatalf("resume result = %+v", res)
	}
	if len(ft.attach) != 1 || ft.attach[0] != res.Workspace.Terminal.Session {
		t.Fatalf("attach = %+v", ft.attach)
	}
}

func TestShelveDirtyCheckoutDoesNotKillTmux(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := app.New(st, fg)
	application.Tmux = ft
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 62), testutil.ID(t, 63)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Dirty Safety", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false); err != nil {
		t.Fatal(err)
	}
	fg.status = " M file.go"
	if _, err := application.Shelve(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("expected dirty checkout refusal, got %v", err)
	}
	m, err := st.LoadManifest(newRes.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != model.StateWorking || !m.Checkout.Present() || m.Checkout.Path == nil {
		t.Fatalf("dirty checkout should remain assigned: %+v", m)
	}
	if len(ft.killed) != 0 {
		t.Fatalf("dirty checkout should be detected before killing tmux: %+v", ft.killed)
	}
}

func TestShelveDoesNotReleaseCheckoutWhenTmuxCloseFails(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{killErr: errors.New("tmux refused")}
	application := app.New(st, fg)
	application.Tmux = ft
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 56), testutil.ID(t, 57)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Close Safety", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Shelve(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false); err == nil || !strings.Contains(err.Error(), "could not close tmux session") {
		t.Fatalf("expected tmux close error, got %v", err)
	}
	m, err := st.LoadManifest(newRes.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != model.StateWorking || !m.Checkout.Present() || m.Checkout.Path == nil {
		t.Fatalf("workspace should remain assigned after tmux close failure: %+v", m)
	}
	if len(ft.killed) != 1 || ft.killed[0] != started.Workspace.Terminal.Session {
		t.Fatalf("killed = %+v", ft.killed)
	}
}

func TestShelveForceFromCurrentTmuxLeavesWorkspaceAssigned(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := app.New(st, fg)
	application.Tmux = ft
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 58), testutil.ID(t, 59)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Current Tmux", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	ft.current = started.Workspace.Terminal.Session
	if _, err := application.Shelve(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID, Env: map[string]string{"TMUX": "/tmp/tmux"}}, false); err == nil || !strings.Contains(err.Error(), "cannot close current tmux session") {
		t.Fatalf("expected current tmux refusal, got %v", err)
	}
	res, err := application.Shelve(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID, Env: map[string]string{"TMUX": "/tmp/tmux"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.State != model.StateBacklog || !m.Checkout.Present() || m.Checkout.Path == nil {
		t.Fatalf("forced shelve from current tmux should keep workspace assigned: %+v", m)
	}
	if len(ft.killed) != 0 {
		t.Fatalf("current tmux session should not be killed from inside itself: %+v", ft.killed)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "\n"), "left current terminal") {
		t.Fatalf("expected current tmux warning: %+v", res.Warnings)
	}
}

func TestArchiveRestoreAndListOrdering(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	application.DeepWorkConfig.MaxActive = 1
	ids := []string{testutil.ID(t, 42), testutil.ID(t, 43), testutil.ID(t, 44), testutil.ID(t, 45), testutil.ID(t, 49)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	a, _ := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "A", CWD: t.TempDir(), DeepWork: true})
	b, _ := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "B", CWD: t.TempDir()})
	c, _ := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "C", CWD: t.TempDir(), DeepWork: true})
	d, _ := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "D", CWD: t.TempDir()})
	if _, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: a.Manifest.ID}, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Archive(context.Background(), app.ResolveOptions{Selector: d.Manifest.ID}, false); err != nil {
		t.Fatal(err)
	}
	list := application.WorkList(app.WorkListOptions{})
	if len(list.Sections.Working) != 1 || list.Sections.Working[0].ID != a.Manifest.ID {
		t.Fatalf("working = %+v", list.Sections.Working)
	}
	if len(list.Sections.Backlog) != 2 || list.Sections.Backlog[0].ID != b.Manifest.ID || list.Sections.Backlog[1].ID != c.Manifest.ID || !list.Sections.Backlog[1].CapacityFull {
		t.Fatalf("backlog = %+v", list.Sections.Backlog)
	}
	archived := application.WorkList(app.WorkListOptions{ArchivedOnly: true})
	if len(archived.Sections.Archived) != 1 || archived.Sections.Archived[0].ID != d.Manifest.ID {
		t.Fatalf("archived = %+v", archived.Sections.Archived)
	}
	restored, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: d.Manifest.ID}, model.StateBacklog, false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != model.StateBacklog {
		t.Fatalf("restored = %+v", restored)
	}
}

func TestWorkListCombinesPositiveAndNegativeLabelRules(t *testing.T) {
	st := store.New(t.TempDir())
	application := app.New(st, newFakeGit(t))
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 73), testutil.ID(t, 74), testutil.ID(t, 75)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	matching, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Matching", CWD: t.TempDir(), Labels: []string{"team", "jira"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Excluded", CWD: t.TempDir(), Labels: []string{"team", "jira", "personal"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Missing", CWD: t.TempDir(), Labels: []string{"team"}}); err != nil {
		t.Fatal(err)
	}
	list := application.WorkList(app.WorkListOptions{LabelRules: map[string]bool{"team": true, "jira": true, "personal": false}})
	if len(list.Sections.Backlog) != 1 || list.Sections.Backlog[0].ID != matching.Manifest.ID {
		t.Fatalf("backlog = %+v", list.Sections.Backlog)
	}
}

func TestArchivedItemsLoseSlugAndDoNotReserveIt(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 52), testutil.ID(t, 53)}
	idx := 0
	application.NewID = func() (string, error) {
		id := ids[idx]
		idx++
		return id, nil
	}
	first, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Reusable Slug", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	firstBranch := first.Manifest.Checkout.Branch
	archived, err := application.Archive(context.Background(), app.ResolveOptions{Selector: first.Manifest.Slug}, false)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Manifest.Slug != "" {
		t.Fatalf("archived slug = %q", archived.Manifest.Slug)
	}
	if _, err := st.Resolve("reusable-slug"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("archived slug should not resolve, err=%v", err)
	}
	second, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Reusable Slug", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if second.Manifest.Slug != "reusable-slug" {
		t.Fatalf("new item slug = %q", second.Manifest.Slug)
	}
	if second.Manifest.Checkout.Branch == firstBranch {
		t.Fatalf("reused slug produced duplicate branch %q", firstBranch)
	}
	restored, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: first.Manifest.ID}, model.StateBacklog, false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Manifest.Slug != "reusable-slug-2" {
		t.Fatalf("restored slug = %q", restored.Manifest.Slug)
	}
	if restored.Manifest.Checkout.Branch != firstBranch {
		t.Fatalf("restoring item renamed branch from %q to %q", firstBranch, restored.Manifest.Checkout.Branch)
	}
}

func TestCheckoutCreateRejectsArchivedItem(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return testutil.ID(t, 60), nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Archived Checkout", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Archive(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := application.CheckoutCreate(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}); err == nil || !strings.Contains(err.Error(), "is archived") {
		t.Fatalf("expected archived checkout refusal, got %v", err)
	}
}

func TestCheckoutStatusReportsBranchMismatch(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 64), testutil.ID(t, 65)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Branch Drift", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false); err != nil {
		t.Fatal(err)
	}
	fg.currentBranch = "main"
	res, err := application.CheckoutStatus(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !res.BranchMismatch || res.CurrentBranch != "main" || res.ExpectedBranch != newRes.Manifest.Checkout.Branch {
		t.Fatalf("branch status = %+v", res)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "\n"), "branch mismatch") {
		t.Fatalf("expected branch mismatch warning: %+v", res.Warnings)
	}
}

func TestSwitchWorkItemMaterializesMissingWorkspaceWithoutChangingState(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := app.New(st, fg)
	application.Tmux = ft
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 52), testutil.ID(t, 53)}
	idx := 0
	application.NewID = func() (string, error) {
		id := ids[idx]
		idx++
		return id, nil
	}
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Tmux Switch Porcelain", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, model.StateWorking, false); err != nil {
		t.Fatal(err)
	}
	switched, err := application.SwitchWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if switched.Manifest.State != model.StateWorking || !switched.Checkout.Present() || switched.Manifest.RootPiSession == nil {
		t.Fatalf("switch porcelain = %+v", switched)
	}
	persisted, err := st.LoadManifest(created.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != model.StateWorking {
		t.Fatalf("switch changed state to %s", persisted.State)
	}
}

func TestSwitchWorkItemDoesNotChangeWaitingState(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := app.New(st, fg)
	application.Tmux = ft
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 54), testutil.ID(t, 55)}
	idx := 0
	application.NewID = func() (string, error) {
		id := ids[idx]
		idx++
		return id, nil
	}
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Switch Me", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false); err != nil {
		t.Fatal(err)
	}
	waited, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, model.StateWaiting, false)
	if err != nil {
		t.Fatal(err)
	}
	if waited.State != model.StateWaiting {
		t.Fatalf("waited = %+v", waited)
	}
	switched, err := application.SwitchWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if switched.Manifest.State != model.StateWaiting {
		t.Fatalf("switch changed state: %+v", switched.Manifest)
	}
	loaded, err := st.LoadManifest(newRes.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != model.StateWaiting {
		t.Fatalf("loaded state = %s", loaded.State)
	}
}

func TestSwitchWorkItemWarnsWhenAlreadyInTargetTmuxSession(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := app.New(st, fg)
	application.Tmux = ft
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 71), testutil.ID(t, 72)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Already Here", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	ft.current = started.Workspace.Terminal.Session
	ft.attach = nil
	switched, err := application.SwitchWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID, Env: map[string]string{"TMUX": "/tmp/tmux"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !switched.Terminal.Attached || len(ft.attach) != 0 {
		t.Fatalf("switch should be a no-op attach=%v calls=%+v", switched.Terminal.Attached, ft.attach)
	}
	if len(switched.Warnings) == 0 || !strings.Contains(strings.Join(switched.Warnings, "\n"), "already in tmux terminal") {
		t.Fatalf("expected already-in-session warning: %+v", switched.Warnings)
	}
}

func TestSwitchWorkItemRejectsBacklog(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	id := testutil.ID(t, 56)
	application.NewID = func() (string, error) { return id, nil }
	res, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Backlog", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.SwitchWorkItem(context.Background(), app.ResolveOptions{Selector: res.Manifest.ID}, false); err == nil {
		t.Fatal("expected backlog switch to fail")
	}
}

func TestCoreWorkspaceAndTmuxAdapterAreIndependent(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := app.New(st, fg)
	application.Tmux = ft
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return testutil.ID(t, 57), nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Independent core and adapter", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := application.EnsureWorkItemWorkspace(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !workspace.Checkout.Present() || len(ft.ensured) != 0 || workspace.Manifest.RootPiSession != nil {
		t.Fatalf("workspace ensure crossed boundaries: workspace=%+v tmux=%+v", workspace, ft.ensured)
	}
	terminal, err := application.EnsureTerminal(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	afterTerminal, err := st.LoadManifest(created.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !terminal.Created || len(ft.ensured) != 1 || afterTerminal.RootPiSession != nil {
		t.Fatalf("terminal ensure crossed agent boundary: terminal=%+v manifest=%+v tmux=%+v", terminal, afterTerminal, ft.ensured)
	}
	if _, err := application.ReleaseWorkItemWorkspace(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false); err == nil || !strings.Contains(err.Error(), "terminal session") {
		t.Fatalf("workspace release should require explicit terminal close: %v", err)
	}
	if _, err := application.CloseTerminal(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}); err != nil {
		t.Fatal(err)
	}
	released, err := application.ReleaseWorkItemWorkspace(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !released.Changed || released.Manifest.Checkout.Present() {
		t.Fatalf("workspace release = %+v", released)
	}
}

func TestLabelAddRemove(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Clock = fixedClock{testutil.Time()}
	id := testutil.ID(t, 46)
	application.NewID = func() (string, error) { return id, nil }
	res, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Labels", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	added, err := application.AddLabels(context.Background(), app.ResolveOptions{Selector: res.Manifest.ID}, []string{"Backend", "Frontend"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(added.Labels, ",") != "backend,frontend" {
		t.Fatalf("labels = %+v", added.Labels)
	}
	removed, err := application.RemoveLabels(context.Background(), app.ResolveOptions{Selector: res.Manifest.ID}, []string{"backend"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed.Labels, ",") != "frontend" {
		t.Fatalf("labels = %+v", removed.Labels)
	}
}

func TestNewWorkItemAllocatesUniqueSlugs(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 50), testutil.ID(t, 51)}
	idx := 0
	application.NewID = func() (string, error) {
		id := ids[idx]
		idx++
		return id, nil
	}
	first, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Duplicate Slug", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Duplicate Slug", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.Slug != "duplicate-slug" || second.Manifest.Slug != "duplicate-slug-2" {
		t.Fatalf("slugs = %q, %q", first.Manifest.Slug, second.Manifest.Slug)
	}
	if first.Manifest.Checkout.Branch != model.ItemBranchName(first.Manifest.Slug, first.Manifest.ID) || second.Manifest.Checkout.Branch != model.ItemBranchName(second.Manifest.Slug, second.Manifest.ID) {
		t.Fatalf("branches = %q, %q", first.Manifest.Checkout.Branch, second.Manifest.Checkout.Branch)
	}
	resolved, err := st.Resolve("duplicate-slug-2")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != second.Manifest.ID {
		t.Fatalf("resolved duplicate-slug-2 to %s, want %s", resolved.ID, second.Manifest.ID)
	}
}

func TestNewWorkItemCreatesBacklogWithoutCheckout(t *testing.T) {
	st := store.New(t.TempDir())
	id := testutil.ID(t, 7)
	now := testutil.Time()
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Clock = fixedClock{now}
	application.NewID = func() (string, error) { return id, nil }

	res, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{
		Title:       "Fix refresh-token race",
		Description: "desc",
		Labels:      []string{"auth", "race"},
		CWD:         t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.ID != id || res.Manifest.Slug != "fix-refresh-token-race" {
		t.Fatalf("manifest identity = %+v", res.Manifest)
	}
	if res.Manifest.Checkout.Present() || res.Manifest.Checkout.Path != nil {
		t.Fatalf("checkout = %+v", res.Manifest.Checkout)
	}
	wantBranch := model.ItemBranchName(res.Manifest.Slug, id)
	if res.Manifest.Checkout.Branch != wantBranch {
		t.Fatalf("branch = %q", res.Manifest.Checkout.Branch)
	}
	if fg.branches[wantBranch] {
		t.Fatalf("branch should not be created until checkout assignment")
	}
	loaded, err := st.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != res.Manifest.Title || loaded.Checkout.Present() {
		t.Fatalf("loaded = %+v", loaded)
	}
	description, err := os.ReadFile(filepath.Join(st.ItemDir(id), model.DescriptionFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(description) != "desc" {
		t.Fatalf("description file = %q", string(description))
	}
	events, err := st.ReadEvents(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "work_item.created" {
		t.Fatalf("events = %+v", events)
	}
}

func TestStartWorkItemLaunchesPrimaryPiSession(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 16)
	sessionID := testutil.ID(t, 17)
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := app.New(st, fg)
	application.Tmux = ft
	application.SelfPath = "/usr/local/bin/wi"
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{itemID, sessionID}
	idx := 0
	application.NewID = func() (string, error) {
		id := ids[idx]
		idx++
		return id, nil
	}
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Pi", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Workspace.Manifest.RootPiSession == nil {
		t.Fatal("root Pi session missing")
	}
	s := *res.Workspace.Manifest.RootPiSession
	if s.ID != sessionID || s.Path != "sessions/pi/"+sessionID+".jsonl" {
		t.Fatalf("session = %+v", s)
	}
	if len(ft.launched) != 1 || !ft.launched[0].ReuseAgentWindow {
		t.Fatalf("launches = %+v", ft.launched)
	}
	runtime, err := st.LoadAgentRuntime(itemID)
	if err != nil || runtime == nil || runtime.Mode != string(agent.ModeTUI) {
		t.Fatalf("agent runtime = %+v err=%v", runtime, err)
	}
	wantCmd := []string{"/usr/local/bin/wi", "agent", "exec", "--item", itemID, "--session", sessionID, "--runtime", runtime.ID, "--mode", "tui"}
	if strings.Join(ft.launched[0].Command, " ") != strings.Join(wantCmd, " ") {
		t.Fatalf("command = %+v, want %+v", ft.launched[0].Command, wantCmd)
	}
	if ft.launched[0].Env["PI_CODING_AGENT_SESSION_DIR"] != filepath.Join(st.ItemDir(itemID), "sessions", "pi") {
		t.Fatalf("Pi session dir env = %q", ft.launched[0].Env["PI_CODING_AGENT_SESSION_DIR"])
	}
	if _, err := os.Stat(filepath.Join(st.ItemDir(itemID), "sessions", "pi", sessionID+".jsonl")); err != nil {
		t.Fatalf("session file missing: %v", err)
	}
	workspace, err := st.LoadTerminalRuntime(itemID)
	if err != nil {
		t.Fatal(err)
	}
	if workspace == nil || workspace.TmuxWindow != "agent" || workspace.TmuxPanePID != 123 || workspace.TmuxPaneID != "%1" {
		t.Fatalf("workspace metadata = %+v", workspace)
	}
}

func TestAgentStatusDerivesBusyFromPiSessionAndProcess(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 35)
	sessionID := testutil.ID(t, 36)
	fg := newFakeGit(t)
	fp := &fakeProcess{alive: map[int]bool{123: true, 456: true}, descendant: processpkg.Info{PID: 456, Command: "pi", Cmdline: []string{"pi"}}, found: true}
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Process = fp
	now := testutil.Time()
	application.Clock = fixedClock{now}
	ids := []string{itemID, sessionID}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Busy Agent", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(st.ItemDir(itemID), started.Workspace.Manifest.RootPiSession.Path)
	appendPiJSONL(t, sessionPath, piMessage(now.Add(-time.Minute), "user", []map[string]any{{"type": "text", "text": "please work"}}))
	status, err := application.AgentStatus(context.Background(), app.AgentStatusOptions{ResolveOptions: app.ResolveOptions{Selector: itemID}, StaleAfter: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "busy" || status.PiSession.InferredTurnState != "incomplete" || !status.Process.Online || status.Process.PiPID != 456 {
		t.Fatalf("status = %+v", status)
	}
}

func TestAgentStatusIgnoresPiSessionMetadataForTurnState(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 76)
	sessionID := testutil.ID(t, 77)
	fg := newFakeGit(t)
	fp := &fakeProcess{alive: map[int]bool{123: true, 456: true}, descendant: processpkg.Info{PID: 456, Command: "pi", Cmdline: []string{"pi"}}, found: true}
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Process = fp
	now := testutil.Time()
	application.Clock = fixedClock{now}
	ids := []string{itemID, sessionID}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Metadata Only Agent", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(st.ItemDir(itemID), started.Workspace.Manifest.RootPiSession.Path)
	appendPiJSONL(t, sessionPath,
		map[string]any{"type": "session", "timestamp": now.Add(-time.Hour).Format(time.RFC3339Nano)},
		map[string]any{"type": "model_change", "timestamp": now.Add(-time.Hour).Format(time.RFC3339Nano)},
		map[string]any{"type": "thinking_level_change", "timestamp": now.Add(-time.Hour).Format(time.RFC3339Nano)},
	)
	status, err := application.AgentStatus(context.Background(), app.AgentStatusOptions{ResolveOptions: app.ResolveOptions{Selector: itemID}, StaleAfter: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "idle" || status.PiSession.InferredTurnState != "idle" || status.PiSession.LastTurnActivity != nil || status.PiSession.LastEvent == nil || status.PiSession.LastEvent.Type != "thinking_level_change" || status.PiSession.LastActivityAgeSeconds != 0 {
		t.Fatalf("status = %+v", status)
	}
}

func TestAttentionQueueRanksRecentRequestsBeforeDeferredItems(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Process = &fakeProcess{alive: map[int]bool{123: true}}
	base := testutil.Time()
	application.Clock = fixedClock{base}
	ids := []string{}
	for i := 100; i < 108; i++ {
		ids = append(ids, testutil.ID(t, byte(i)))
	}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	type createdItem struct {
		id, sessionPath string
	}
	items := map[string]createdItem{}
	for _, name := range []string{"A", "E", "F", "B"} {
		created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: name, Labels: []string{strings.ToLower(name)}, CWD: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		items[name] = createdItem{id: created.Manifest.ID, sessionPath: filepath.Join(st.ItemDir(created.Manifest.ID), started.Workspace.Manifest.RootPiSession.Path)}
	}
	minutes := map[string]int{"A": 1, "E": 3, "F": 2, "B": 8}
	for name, item := range items {
		requested := base.Add(time.Duration(minutes[name]) * time.Minute)
		appendPiJSONL(t, item.sessionPath,
			piMessage(requested, "user", []map[string]any{{"type": "text", "text": "work"}}),
			piMessage(requested.Add(time.Second), "assistant", []map[string]any{{"type": "text", "text": "done"}}),
		)
	}
	application.Clock = fixedClock{base.Add(10 * time.Minute)}
	deferred, err := application.DeferWorkItem(context.Background(), app.ResolveOptions{Selector: items["A"].id})
	if err != nil || deferred.Activity.LastDeferredAt == nil {
		t.Fatalf("defer result=%+v err=%v", deferred, err)
	}
	queue, err := application.AttentionQueue(context.Background(), app.AttentionQueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, candidate := range queue.Candidates {
		got = append(got, candidate.Item.Title)
	}
	if strings.Join(got, ",") != "B,E,F,A" {
		t.Fatalf("queue = %v", got)
	}
	appendPiJSONL(t, items["A"].sessionPath,
		piMessage(base.Add(11*time.Minute), "user", []map[string]any{{"type": "text", "text": "more work"}}),
		piMessage(base.Add(11*time.Minute+time.Second), "assistant", []map[string]any{{"type": "text", "text": "more done"}}),
	)
	application.Clock = fixedClock{base.Add(12 * time.Minute)}
	queue, err = application.AttentionQueue(context.Background(), app.AttentionQueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got = got[:0]
	for _, candidate := range queue.Candidates {
		got = append(got, candidate.Item.Title)
	}
	if strings.Join(got, ",") != "A,B,E,F" || queue.Candidates[0].Activity.LastDeferredAt == nil {
		t.Fatalf("queue after new request = %+v", queue.Candidates)
	}

	next, err := application.NextWorkItem(context.Background(), app.NextWorkItemOptions{ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": items["A"].id}}}, false)
	if err != nil || next.Selected.Item.Title != "B" || !next.CurrentInQueue || next.Wrapped {
		t.Fatalf("next after A = %+v err=%v", next, err)
	}
	next, err = application.NextWorkItem(context.Background(), app.NextWorkItemOptions{ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": items["F"].id}}}, false)
	if err != nil || next.Selected.Item.Title != "A" || !next.Wrapped {
		t.Fatalf("next after F = %+v err=%v", next, err)
	}
	next, err = application.NextWorkItem(context.Background(), app.NextWorkItemOptions{}, false)
	if err != nil || next.Selected.Item.Title != "A" || next.CurrentInQueue || next.CurrentWorkItemID != "" {
		t.Fatalf("next without current item = %+v err=%v", next, err)
	}
	next, err = application.NextWorkItem(context.Background(), app.NextWorkItemOptions{
		ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": items["E"].id}},
		DeferCurrent:   true,
	}, false)
	if err != nil || next.Deferred == nil || next.Deferred.WorkItemID != items["E"].id || next.Selected.Item.Title != "A" || !next.Wrapped {
		t.Fatalf("next --defer from E = %+v err=%v", next, err)
	}
	mux := application.Tmux.(*fakeTmux)
	mux.ensureErr = errors.New("switch failed")
	partial, err := application.NextWorkItem(context.Background(), app.NextWorkItemOptions{
		ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": items["E"].id}},
		DeferCurrent:   true,
	}, false)
	if err == nil || partial.Deferred == nil || !strings.Contains(err.Error(), "deferred "+items["E"].id+", but could not switch") {
		t.Fatalf("partial next result = %+v err=%v", partial, err)
	}
	mux.ensureErr = nil
	appendPiJSONL(t, items["B"].sessionPath,
		piMessage(base.Add(13*time.Minute), "user", []map[string]any{{"type": "text", "text": "new busy turn"}}),
	)
	application.Clock = fixedClock{base.Add(14 * time.Minute)}
	next, err = application.NextWorkItem(context.Background(), app.NextWorkItemOptions{ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": items["B"].id}}, DeferCurrent: true}, false)
	if err != nil || next.Selected.Item.Title != "A" || next.CurrentInQueue || next.Deferred != nil {
		t.Fatalf("next --defer from busy item = %+v err=%v", next, err)
	}
	if _, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: items["B"].id}, model.StateWaiting, false); err != nil {
		t.Fatal(err)
	}
	next, err = application.NextWorkItem(context.Background(), app.NextWorkItemOptions{ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": items["B"].id}}, DeferCurrent: true}, false)
	if err != nil || next.Selected.Item.Title != "A" || next.CurrentInQueue || next.CurrentWorkItemID != items["B"].id || next.Deferred != nil {
		t.Fatalf("next --defer from waiting item = %+v err=%v", next, err)
	}
	next, err = application.NextWorkItem(context.Background(), app.NextWorkItemOptions{
		ResolveOptions:  app.ResolveOptions{Env: map[string]string{"WI_ID": items["A"].id}},
		WorkListOptions: app.WorkListOptions{LabelRules: map[string]bool{"e": true}},
		DeferCurrent:    true,
	}, false)
	if err != nil || next.Selected.Item.Title != "E" || next.CurrentInQueue || next.Deferred == nil || next.Deferred.WorkItemID != items["A"].id {
		t.Fatalf("next --defer with label-filtered current = %+v err=%v", next, err)
	}
	precomputed := app.AttentionQueueResult{Candidates: []app.AttentionCandidate{{Item: app.WorkListItem{ID: items["E"].id, Slug: "e", Title: "E", State: model.StateWorking}, Rank: 1}}}
	preselection := app.NextQueueSelection{Index: 0, WorkItemID: items["E"].id}
	next, err = application.NextWorkItem(context.Background(), app.NextWorkItemOptions{
		ResolveOptions:       app.ResolveOptions{Env: map[string]string{"WI_ID": items["A"].id}},
		PrecomputedQueue:     &precomputed,
		PrecomputedSelection: &preselection,
	}, false)
	if err != nil || next.Selected.Item.Title != "E" {
		t.Fatalf("next with precomputed queue = %+v err=%v", next, err)
	}
	invalidSelection := preselection
	invalidSelection.WorkItemID = items["A"].id
	if _, err := application.NextWorkItem(context.Background(), app.NextWorkItemOptions{ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": items["A"].id}}, PrecomputedQueue: &precomputed, PrecomputedSelection: &invalidSelection}, false); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected mismatched daemon selection rejection, got %v", err)
	}
	next, err = application.NextWorkItem(context.Background(), app.NextWorkItemOptions{
		ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": items["F"].id}},
		WaitCurrent:    true,
	}, false)
	if err != nil || next.Waited == nil || next.Waited.State != model.StateWaiting || next.Selected.Item.Title != "E" || next.CurrentInQueue {
		t.Fatalf("next --wait from F = %+v err=%v", next, err)
	}
	waitingManifest, err := st.LoadManifest(items["F"].id)
	if err != nil || waitingManifest.State != model.StateWaiting {
		t.Fatalf("waited manifest = %+v err=%v", waitingManifest, err)
	}
	waitingEvents, err := st.ReadEvents(items["F"].id)
	if err != nil {
		t.Fatal(err)
	}
	waitEvent := waitingEvents[len(waitingEvents)-1]
	if waitEvent.Type != "work_item.state_set" || waitEvent.Data["workspace_unchanged"] != true {
		t.Fatalf("next --wait did not use the canonical state primitive: %+v", waitEvent)
	}
	events, err := st.ReadEvents(items["A"].id)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "attention.selected" {
			t.Fatalf("next must not record selection events: %+v", events)
		}
	}
}

func TestArchiveStopsRuntimeBeforeArchiving(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wi-archive-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	st := store.New(root)
	fg := newFakeGit(t)
	proc := &fakeProcess{alive: map[int]bool{4321: true}}
	application := app.New(st, fg)
	application.Process = proc
	application.Clock = fixedClock{testutil.Time()}
	itemID := testutil.ID(t, 77)
	application.NewID = func() (string, error) { return itemID, nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Archive Runtime", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := model.AgentRuntime{ID: "runtime-archive", WorkItemID: itemID, Mode: string(agent.ModeRPC), HostPID: 4321, HostProcessGroup: 4321, HostStartTime: 4321}
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
	archived, err := application.Archive(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if archived.State != model.StateArchived || !containsString(archived.Warnings, "requested agent runtime shutdown") {
		t.Fatalf("archived = %+v", archived)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestNextWorkItemArchivePersistsBeforeSchedulingPreviousTerminalClose(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	mux := &fakeTmux{}
	application := app.New(st, fg)
	application.Tmux = mux
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 81), testutil.ID(t, 82), testutil.ID(t, 83), testutil.ID(t, 84)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	created := []app.NewWorkItemResult{}
	for _, title := range []string{"Archive Current", "Archive Successor"} {
		item, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: title, CWD: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: item.Manifest.ID}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		appendPiJSONL(t, filepath.Join(st.ItemDir(item.Manifest.ID), started.Workspace.Manifest.RootPiSession.Path),
			piMessage(testutil.Time().Add(time.Minute), "user", []map[string]any{{"type": "text", "text": "work"}}),
			piMessage(testutil.Time().Add(time.Minute+time.Second), "assistant", []map[string]any{{"type": "text", "text": "done"}}),
		)
		created = append(created, item)
	}
	result, err := application.NextWorkItem(context.Background(), app.NextWorkItemOptions{
		ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": created[0].Manifest.ID, "TMUX": "/tmp/tmux"}},
		ArchiveCurrent: true,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected.Item.ID != created[1].Manifest.ID || result.Archived == nil || result.Archived.WorkItemID != created[0].Manifest.ID {
		t.Fatalf("result = %+v", result)
	}
	archived, err := st.LoadManifest(created[0].Manifest.ID)
	if err != nil || archived.State != model.StateArchived || archived.Slug != "" || archived.Checkout.Present() {
		t.Fatalf("archived = %+v err=%v", archived, err)
	}
	if len(mux.killed) != 0 || len(mux.killedAsync) != 1 || mux.killedAsync[0] != created[0].Manifest.TerminalSessionName() {
		t.Fatalf("synchronous kills=%+v scheduled kills=%+v", mux.killed, mux.killedAsync)
	}
}

func TestNextWorkItemArchiveJSONModeDoesNotPretendTmuxSwitched(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	mux := &fakeTmux{}
	application := app.New(st, fg)
	application.Tmux = mux
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 89), testutil.ID(t, 90), testutil.ID(t, 91), testutil.ID(t, 92)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	created := []app.NewWorkItemResult{}
	for _, title := range []string{"JSON Current", "JSON Successor"} {
		item, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: title, CWD: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: item.Manifest.ID}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		appendPiJSONL(t, filepath.Join(st.ItemDir(item.Manifest.ID), started.Workspace.Manifest.RootPiSession.Path),
			piMessage(testutil.Time().Add(time.Minute), "user", []map[string]any{{"type": "text", "text": "work"}}),
			piMessage(testutil.Time().Add(time.Minute+time.Second), "assistant", []map[string]any{{"type": "text", "text": "done"}}),
		)
		created = append(created, item)
	}
	mux.current = created[0].Manifest.TerminalSessionName()
	partial, err := application.NextWorkItem(context.Background(), app.NextWorkItemOptions{ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": created[0].Manifest.ID, "TMUX": "/tmp/tmux"}}, ArchiveCurrent: true}, false)
	if err == nil || partial.Workspace.WorkItemID != created[1].Manifest.ID || !strings.Contains(err.Error(), "cannot close current tmux session") {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
	current, loadErr := st.LoadManifest(created[0].Manifest.ID)
	if loadErr != nil || current.State == model.StateArchived {
		t.Fatalf("current=%+v err=%v", current, loadErr)
	}
}

func TestNextWorkItemArchivePreflightFailsBeforeSwitching(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 85), testutil.ID(t, 86), testutil.ID(t, 87), testutil.ID(t, 88)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	created := []app.NewWorkItemResult{}
	for _, title := range []string{"Dirty Current", "Clean Successor"} {
		item, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: title, CWD: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: item.Manifest.ID}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		appendPiJSONL(t, filepath.Join(st.ItemDir(item.Manifest.ID), started.Workspace.Manifest.RootPiSession.Path),
			piMessage(testutil.Time().Add(time.Minute), "user", []map[string]any{{"type": "text", "text": "work"}}),
			piMessage(testutil.Time().Add(time.Minute+time.Second), "assistant", []map[string]any{{"type": "text", "text": "done"}}),
		)
		created = append(created, item)
	}
	fg.status = " M dirty.go"
	partial, err := application.NextWorkItem(context.Background(), app.NextWorkItemOptions{ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": created[0].Manifest.ID}}, ArchiveCurrent: true}, false)
	if err == nil || partial.Workspace.WorkItemID != "" || !strings.Contains(err.Error(), "could not prepare current item "+created[0].Manifest.ID+" for archive before switching") {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
	current, loadErr := st.LoadManifest(created[0].Manifest.ID)
	if loadErr != nil || current.State == model.StateArchived {
		t.Fatalf("current=%+v err=%v", current, loadErr)
	}
}

func TestNextWorkItemFailsForEmptyAttentionRing(t *testing.T) {
	application := app.New(store.New(t.TempDir()), newFakeGit(t))
	if _, err := application.NextWorkItem(context.Background(), app.NextWorkItemOptions{}, false); err == nil || !strings.Contains(err.Error(), "nothing needs attention") {
		t.Fatalf("error = %v", err)
	}
}

func TestAgentStatusDerivesIdleWithChangedWorktreeFromFinalAnswerAndGitChanges(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 37)
	sessionID := testutil.ID(t, 38)
	fg := newFakeGit(t)
	fg.status = " M file.go"
	fp := &fakeProcess{alive: map[int]bool{123: true}}
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Process = fp
	now := testutil.Time()
	application.Clock = fixedClock{now}
	ids := []string{itemID, sessionID}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Ready Agent", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(st.ItemDir(itemID), started.Workspace.Manifest.RootPiSession.Path)
	appendPiJSONL(t, sessionPath,
		piMessage(now.Add(-2*time.Minute), "user", []map[string]any{{"type": "text", "text": "please implement"}}),
		piMessage(now.Add(-time.Minute), "assistant", []map[string]any{{"type": "text", "text": "Implemented and tested."}}),
	)
	status, err := application.AgentStatus(context.Background(), app.AgentStatusOptions{ResolveOptions: app.ResolveOptions{Selector: itemID}})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "idle" || status.PiSession.InferredTurnState != "idle" || status.Worktree == nil || status.Worktree.Status != "changed" || !status.Worktree.HasChanges {
		t.Fatalf("status = %+v", status)
	}
}

func TestAgentStatusDerivesProblemWhenIncompleteAndOffline(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 39)
	sessionID := testutil.ID(t, 40)
	fg := newFakeGit(t)
	fp := &fakeProcess{alive: map[int]bool{}}
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Process = fp
	now := testutil.Time()
	application.Clock = fixedClock{now}
	ids := []string{itemID, sessionID}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Stale Agent", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(st.ItemDir(itemID), started.Workspace.Manifest.RootPiSession.Path)
	appendPiJSONL(t, sessionPath, piMessage(now.Add(-time.Hour), "assistant", []map[string]any{{"type": "toolCall"}}))
	status, err := application.AgentStatus(context.Background(), app.AgentStatusOptions{ResolveOptions: app.ResolveOptions{Selector: itemID}, StaleAfter: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "problem" {
		t.Fatalf("status = %+v", status)
	}
}

func TestStartRecreatedTmuxResumesRootPiSession(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 25)
	sessionID := testutil.ID(t, 26)
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := app.New(st, fg)
	application.Tmux = ft
	application.SelfPath = "wi"
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{itemID, sessionID}
	idx := 0
	application.NewID = func() (string, error) {
		id := ids[idx]
		idx++
		return id, nil
	}
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Start Resume", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false); err != nil {
		t.Fatal(err)
	}
	ft.launched = nil
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: itemID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !started.Workspace.Terminal.Created || !started.Workspace.PiLaunched || started.Workspace.PiSession == nil || started.Workspace.PiSession.ID != sessionID {
		t.Fatalf("start result = %+v", started)
	}
	if len(ft.launched) != 1 || !ft.launched[0].ReuseAgentWindow {
		t.Fatalf("launches = %+v", ft.launched)
	}
}

func TestHeadlessRPCRuntimeUsesNativeControlWithoutTmux(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 111)
	sessionID := testutil.ID(t, 112)
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	fp := &fakeProcess{alive: map[int]bool{4321: true}}
	launcher := &fakeRuntimeLauncher{pid: 4321}
	application := app.New(st, fg)
	application.Tmux = ft
	application.Process = fp
	application.AgentRuntimeLauncher = launcher
	application.SelfPath = "/usr/local/bin/wi"
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{itemID, sessionID}
	idx := 0
	application.NewID = func() (string, error) {
		id := ids[idx]
		idx++
		return id, nil
	}
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Headless", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: itemID}, model.StateWorking, false); err != nil {
		t.Fatal(err)
	}
	ensured, err := application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, agent.ModeRPC)
	if err != nil {
		t.Fatal(err)
	}
	if !ensured.Created || ensured.Runtime.Mode != "rpc" || ensured.Runtime.HostPID != 4321 || len(ft.ensured) != 0 || len(ft.launched) != 0 {
		t.Fatalf("runtime = %+v tmux ensured=%d launched=%d", ensured, len(ft.ensured), len(ft.launched))
	}
	if strings.Join(launcher.spec.Args, " ") != strings.Join([]string{"agent", "exec", "--item", itemID, "--session", sessionID, "--runtime", ensured.Runtime.ID, "--mode", "rpc"}, " ") {
		t.Fatalf("launcher args = %+v", launcher.spec.Args)
	}
	if _, err := application.SwitchWorkItem(context.Background(), app.ResolveOptions{Selector: itemID}, false); err == nil || !strings.Contains(err.Error(), "currently owns the conversation in rpc mode") {
		t.Fatalf("expected safe TUI handoff refusal, got %v", err)
	}
}

func TestEnsureAgentRuntimeForksConversationAfterSlotReassignment(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 123)
	sourceSessionID := testutil.ID(t, 124)
	forkedSessionID := testutil.ID(t, 125)
	application := app.New(st, newFakeGit(t))
	application.Tmux = &fakeTmux{}
	application.Pi = &fakePi{}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{itemID, sourceSessionID, forkedSessionID}
	application.NewID = func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Fork moved session", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: itemID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	manifest := started.Workspace.Manifest
	currentCheckout := *manifest.Checkout.Path
	sourcePath := filepath.Join(st.ItemDir(itemID), filepath.FromSlash(manifest.RootPiSession.Path))
	oldCheckout := filepath.Join(filepath.Dir(currentCheckout), "slot-0004")
	source := fmt.Sprintf("{\"type\":\"session\",\"version\":3,\"id\":\"11111111-1111-4111-8111-111111111111\",\"timestamp\":\"2026-08-03T07:48:31.948Z\",\"cwd\":%q}\n{\"type\":\"message\",\"id\":\"abcd1234\",\"parentId\":null,\"timestamp\":\"2026-08-03T07:49:00Z\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n", oldCheckout)
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	ensured, err := application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, agent.ModeTUI)
	if err != nil {
		t.Fatal(err)
	}
	if ensured.Conversation.ID != forkedSessionID || ensured.Conversation.Path == manifest.RootPiSession.Path {
		t.Fatalf("conversation = %+v", ensured.Conversation)
	}
	forkedPath := filepath.Join(st.ItemDir(itemID), filepath.FromSlash(ensured.Conversation.Path))
	if cwd, err := application.Pi.SessionCWD(forkedPath); err != nil || cwd != currentCheckout {
		t.Fatalf("forked cwd=%q err=%v", cwd, err)
	}
	data, err := os.ReadFile(forkedPath)
	if err != nil || !strings.Contains(string(data), "abcd1234") || !strings.Contains(string(data), sourcePath) {
		t.Fatalf("forked data=%q err=%v", data, err)
	}
}

func TestEnsureAgentRuntimeRejectsTUIInDifferentCheckout(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 121)
	application := app.New(st, newFakeGit(t))
	ft := &fakeTmux{}
	application.Tmux = ft
	application.Process = &fakeProcess{alive: map[int]bool{4321: true}}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{itemID, testutil.ID(t, 122)}
	application.NewID = func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Moved checkout", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: itemID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	runtime := *started.Workspace.AgentRuntime
	runtime.HostPID = 4321
	runtime.HostProcessGroup = 4321
	runtime.HostStartTime = 4321
	runtime.State = model.AgentRuntimeRunning
	if err := st.SaveAgentRuntime(context.Background(), itemID, runtime); err != nil {
		t.Fatal(err)
	}
	ft.panePath = filepath.Join(t.TempDir(), "slot-0004")
	_, err = application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, agent.ModeTUI)
	if err == nil || !strings.Contains(err.Error(), "now owns checkout") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewAgentWorkItemStartsDetachedRPCAndSubmitsDescription(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wi-new-agent-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	st := store.New(root)
	itemID := testutil.ID(t, 115)
	sessionID := testutil.ID(t, 116)
	requestID := testutil.ID(t, 117)
	fg := newFakeGit(t)
	fp := &fakeProcess{alive: map[int]bool{4321: true}}
	var control *agent.ControlSocketServer
	launcher := &fakeRuntimeLauncher{pid: 4321}
	application := app.New(st, fg)
	application.Process = fp
	application.AgentRuntimeLauncher = launcher
	application.AgentRuntimeSocketRoot = filepath.Join(root, "runtime")
	application.AgentRuntimeStateRoot = filepath.Join(root, "state")
	application.SelfPath = "/usr/local/bin/wi"
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{itemID, sessionID, requestID}
	application.NewID = func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	launcher.onStart = func(spec agent.LaunchSpec) error {
		runtime, err := st.LoadAgentRuntime(itemID)
		if err != nil || runtime == nil {
			return fmt.Errorf("load prepared runtime: %v", err)
		}
		socketPath := filepath.Join(application.AgentRuntimeSocketRoot, filepath.FromSlash(runtimepath.ControlSocket(itemID, runtime.ID)))
		control, err = agent.ListenControlSocket(socketPath)
		if err != nil {
			return err
		}
		go func() {
			for request := range control.Requests() {
				request.Respond(nil)
			}
		}()
		return nil
	}
	defer func() {
		if control != nil {
			_ = control.Close()
		}
	}()

	prompt := "Investigate the refresh race and add a regression test."
	result, err := application.NewAgentWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Refresh race", Description: prompt, CWD: t.TempDir()}, agent.ModeRPC, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if result.Start.Transition.State != model.StateWorking || result.Start.Workspace.AgentRuntime == nil || result.Start.Workspace.AgentRuntime.Mode != string(agent.ModeRPC) || result.Start.Workspace.Terminal != nil {
		t.Fatalf("start result = %+v", result.Start)
	}
	if !result.Control.Submitted || result.Control.Command.Message != prompt || result.Control.Command.Actor != "planner" {
		t.Fatalf("control result = %+v", result.Control)
	}
	shown, err := application.ShowWorkItem(context.Background(), app.ResolveOptions{Selector: itemID})
	if err != nil {
		t.Fatal(err)
	}
	if shown.Description != prompt || shown.Manifest.State != model.StateWorking {
		t.Fatalf("shown = %+v", shown)
	}
	if launcher.spec.CWD == "" || strings.Join(launcher.spec.Args, " ") == "" {
		t.Fatalf("launcher spec = %+v", launcher.spec)
	}
}

func TestNewAgentWorkItemDefaultsToDetachedTUIWithoutAttaching(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wi-new-tui-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	st := store.New(root)
	itemID := testutil.ID(t, 118)
	fp := &fakeProcess{alive: map[int]bool{4321: true}}
	ft := &fakeTmux{}
	var control *agent.ControlSocketServer
	application := app.New(st, newFakeGit(t))
	application.Process = fp
	application.Tmux = ft
	application.AgentRuntimeSocketRoot = filepath.Join(root, "runtime")
	application.AgentRuntimeStateRoot = filepath.Join(root, "state")
	application.SelfPath = "/usr/local/bin/wi"
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{itemID, testutil.ID(t, 119), testutil.ID(t, 120)}
	application.NewID = func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	ft.onLaunch = func(spec tmuxpkg.LaunchSpec) error {
		runtime, err := st.LoadAgentRuntime(itemID)
		if err != nil || runtime == nil {
			return fmt.Errorf("load prepared runtime: %v", err)
		}
		runtime.HostPID = 4321
		runtime.HostProcessGroup = 4321
		runtime.HostStartTime = 4321
		runtime.State = model.AgentRuntimeRunning
		if err := st.SaveAgentRuntime(context.Background(), itemID, *runtime); err != nil {
			return err
		}
		control, err = agent.ListenControlSocket(filepath.Join(application.AgentRuntimeSocketRoot, filepath.FromSlash(runtimepath.ControlSocket(itemID, runtime.ID))))
		if err != nil {
			return err
		}
		go func() {
			for request := range control.Requests() {
				request.Respond(nil)
			}
		}()
		return nil
	}
	defer func() {
		if control != nil {
			_ = control.Close()
		}
	}()

	result, err := application.NewAgentWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Detached TUI", Description: "Implement the requested change.", CWD: t.TempDir()}, "", "planner")
	if err != nil {
		t.Fatal(err)
	}
	if result.Start.Workspace.AgentRuntime == nil || result.Start.Workspace.AgentRuntime.Mode != string(agent.ModeTUI) || result.Start.Workspace.Terminal == nil || len(ft.launched) != 1 {
		t.Fatalf("start result = %+v launched=%d", result.Start, len(ft.launched))
	}
	if len(ft.attach) != 0 {
		t.Fatalf("unexpected tmux attachment: %+v", ft.attach)
	}
	if !result.Control.Submitted {
		t.Fatalf("control result = %+v", result.Control)
	}
}

func TestAgentExecHoldsLockAndRunsPiWithExplicitRootSessionPath(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 21)
	sessionID := testutil.ID(t, 22)
	fg := newFakeGit(t)
	fp := &fakePi{}
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Pi = fp
	application.AgentRuntimeStateRoot = filepath.Join(t.TempDir(), "state")
	application.AgentRuntimeSocketRoot = filepath.Join(t.TempDir(), "runtime")
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{itemID, sessionID}
	idx := 0
	application.NewID = func() (string, error) {
		id := ids[idx]
		idx++
		return id, nil
	}
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Exec", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Workspace.Manifest.RootPiSession == nil {
		t.Fatal("root Pi session missing")
	}
	manifest, err := st.LoadManifest(itemID)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := st.LoadAgentRuntime(itemID)
	if err != nil || runtime == nil {
		t.Fatalf("agent runtime = %+v err=%v", runtime, err)
	}
	if err := application.AgentExec(context.Background(), itemID, res.Workspace.Manifest.RootPiSession.ID, runtime.ID, agent.ModeTUI); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(st.ItemDir(itemID), "sessions", "pi", sessionID+".jsonl")
	if fp.sessionPath != wantPath {
		t.Fatalf("pi session path = %q, want %q", fp.sessionPath, wantPath)
	}
	if manifest.Checkout.Path == nil || fp.cwd != *manifest.Checkout.Path {
		t.Fatalf("pi cwd = %q, checkout = %+v", fp.cwd, manifest.Checkout)
	}
	if fp.env["WI_ID"] != itemID || fp.env["WI_DIR"] != st.ItemDir(itemID) {
		t.Fatalf("env = %+v", fp.env)
	}
	if fp.env["PI_CODING_AGENT_SESSION_DIR"] != filepath.Join(st.ItemDir(itemID), "sessions", "pi") {
		t.Fatalf("Pi session dir env = %q", fp.env["PI_CODING_AGENT_SESSION_DIR"])
	}
	if fp.logPath != filepath.Join(application.AgentRuntimeStateRoot, filepath.FromSlash(runtimepath.DiagnosticLog(itemID, runtime.ID))) {
		t.Fatalf("runtime log path = %q", fp.logPath)
	}
	if fp.controlPath != filepath.Join(application.AgentRuntimeSocketRoot, filepath.FromSlash(runtimepath.ControlSocket(itemID, runtime.ID))) {
		t.Fatalf("control socket path = %q", fp.controlPath)
	}
}

func TestAgentExecRefusesLockedRootSession(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 23)
	sessionID := testutil.ID(t, 24)
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Pi = &fakePi{}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{itemID, sessionID}
	idx := 0
	application.NewID = func() (string, error) {
		id := ids[idx]
		idx++
		return id, nil
	}
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Locked", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Workspace.Manifest.RootPiSession == nil {
		t.Fatal("root Pi session missing")
	}
	lockFile, err := os.OpenFile(filepath.Join(st.ItemDir(itemID), "locks", "pi-"+res.Workspace.Manifest.RootPiSession.ID+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	runtime, err := st.LoadAgentRuntime(itemID)
	if err != nil || runtime == nil {
		t.Fatalf("agent runtime = %+v err=%v", runtime, err)
	}
	if err := application.AgentExec(context.Background(), itemID, res.Workspace.Manifest.RootPiSession.ID, runtime.ID, agent.ModeTUI); err == nil {
		t.Fatal("expected locked session exec to fail")
	}
}

func TestArchiveRepositoryHomeReleasesClaimWithoutTouchingCheckout(t *testing.T) {
	st := store.New(t.TempDir())
	root := t.TempDir()
	fg := newFakeGit(t)
	fg.info.Repository.RootAtCreation = root
	fg.info.Repository.GitCommonDir = filepath.Join(root, ".git")
	fg.worktrees[root] = "main"
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 101), testutil.ID(t, 102)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Mainline", Home: true, CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := application.Archive(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	checkout := archived.Manifest.Checkout
	if checkout.Kind != model.WorkspaceKindRepositoryHome || checkout.Present() || checkout.Path != nil {
		t.Fatalf("checkout=%+v", checkout)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("repository home was touched: %v", err)
	}
	if _, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Next mainline", Home: true, CWD: root}); err != nil {
		t.Fatalf("new home claim after archive: %v", err)
	}
}

func TestNewWorkItemUsesStableReadableBranch(t *testing.T) {
	st := store.New(t.TempDir())
	id := testutil.ID(t, 8)
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return id, nil }
	res, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Fix Race", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := model.ItemBranchName(res.Manifest.Slug, id)
	if res.Manifest.Checkout.Branch != want {
		t.Fatalf("branch = %q, want %q", res.Manifest.Checkout.Branch, want)
	}
}

func TestStartWorkItemReturnsTmuxCreationFailure(t *testing.T) {
	st := store.New(t.TempDir())
	id := testutil.ID(t, 15)
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Tmux = &fakeTmux{ensureErr: errors.New("tmux failed")}
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return id, nil }
	res, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Workspace Failure", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: res.Manifest.ID}, false, false); err == nil {
		t.Fatal("expected tmux failure")
	}
	if _, err := os.Stat(st.ItemDir(id)); err != nil {
		t.Fatalf("item directory should remain after start failure, stat err=%v", err)
	}
	loaded, err := st.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Checkout.Path == nil {
		t.Fatalf("checkout should have been assigned before tmux failure: %+v", loaded.Checkout)
	}
	if _, err := os.Stat(*loaded.Checkout.Path); err != nil {
		t.Fatalf("slot should remain after start failure, stat err=%v", err)
	}
}

func TestCheckoutRemoveDoesNotArchiveAndCreateReusesBranch(t *testing.T) {
	st := store.New(t.TempDir())
	id := testutil.ID(t, 10)
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return id, nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Checkout", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := application.CheckoutCreate(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	initialPath := *initial.Checkout.Path
	if initial.Checkout.Kind != model.WorkspaceKindManagedSlot {
		t.Fatalf("assigned checkout kind = %q", initial.Checkout.Kind)
	}
	removed, err := application.CheckoutRemove(context.Background(), app.ResolveOptions{Selector: id}, false)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Manifest.State == model.StateArchived || removed.Checkout.Present() || removed.Checkout.Path != nil {
		t.Fatalf("removed = %+v", removed)
	}
	if _, err := os.Stat(initialPath); err != nil {
		t.Fatalf("slot should remain after checkout release: %v", err)
	}
	created, err := application.CheckoutCreate(context.Background(), app.ResolveOptions{Selector: id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(created.Source, "branch "+newRes.Manifest.Checkout.Branch) {
		t.Fatalf("source = %q", created.Source)
	}
	if !created.Checkout.Present() || created.Checkout.Path == nil {
		t.Fatalf("created checkout = %+v", created.Checkout)
	}
}

func TestCheckoutRemoveRefusesDirtyWithoutForce(t *testing.T) {
	st := store.New(t.TempDir())
	id := testutil.ID(t, 11)
	fg := newFakeGit(t)
	application := app.New(st, fg)
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return id, nil }
	if _, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Dirty", CWD: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.CheckoutCreate(context.Background(), app.ResolveOptions{Selector: id}); err != nil {
		t.Fatal(err)
	}
	fg.status = " M file.go\n"
	if _, err := application.CheckoutRemove(context.Background(), app.ResolveOptions{Selector: id}, false); err == nil {
		t.Fatal("expected dirty checkout release to fail")
	}
	if _, err := application.CheckoutRemove(context.Background(), app.ResolveOptions{Selector: id}, true); err == nil {
		t.Fatal("expected forced dirty slot release to fail")
	}
}

func TestResolveItemFromTmuxEnvironment(t *testing.T) {
	st := store.New(t.TempDir())
	id := testutil.ID(t, 12)
	fg := newFakeGit(t)
	ft := &fakeTmux{current: "wi-session", env: map[string]string{"WI_ID": id}}
	application := app.New(st, fg)
	application.Tmux = ft
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return id, nil }
	if _, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Tmux", CWD: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	resolved, err := application.ResolveItem(context.Background(), app.ResolveOptions{Env: map[string]string{"TMUX": "/tmp/tmux"}, CWD: filepath.Join(t.TempDir(), "elsewhere")})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != id {
		t.Fatalf("resolved id = %s", resolved.ID)
	}
}

func repoInfo(t *testing.T) gitpkg.RepositoryInfo {
	t.Helper()
	commit := strings.Repeat("a", 40)
	root := t.TempDir()
	return gitpkg.RepositoryInfo{
		Repository: model.Repository{
			RootAtCreation:    root,
			GitCommonDir:      root + "/.git",
			RemoteURL:         "git@example.com:acme/repo.git",
			CreatedFromCommit: commit,
		},
		Commit: commit,
	}
}
