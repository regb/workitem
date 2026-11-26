package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	piadapter "github.com/regb/workitem/internal/agentdriver/pi"
	"github.com/regb/workitem/internal/app"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	processpkg "github.com/regb/workitem/internal/process"
	"github.com/regb/workitem/internal/store"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
)

func newTestApp(st *store.Store, git app.Git) *app.App {
	application := app.New(st, git)
	application.StaticAgentProfiles.Default = "pi"
	application.StaticAgentProfiles.Profiles = append(application.StaticAgentProfiles.Profiles, model.AgentProfile{Name: "pi", Driver: "pi", Config: map[string]any{}})
	application.ConversationBackends["pi"] = fakeConversationBackend{}
	application.ConversationProjectionObserver = piadapter.ObserveConversation(st.ItemDir)
	return application
}

// sequenceIDs makes the app hand out the given IDs in order.
func sequenceIDs(t *testing.T, application *app.App, ids ...string) {
	t.Helper()
	application.NewID = func() (string, error) {
		if len(ids) == 0 {
			t.Fatal("ran out of test IDs")
		}
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
}

// createItem creates a backlog item the way the daemon does: the app
// prepares it and the structured writer (the coordinator in production)
// persists it.
func createItem(t *testing.T, application *app.App, opts app.NewWorkItemOptions) model.Manifest {
	t.Helper()
	ctx := context.Background()
	if opts.CWD == "" {
		opts.CWD = t.TempDir()
	}
	prepared, err := application.PrepareNewWorkItem(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	m := prepared.Manifest
	created := model.NewEvent(m.CreatedAt, "work_item.created", "wi", map[string]any{"title": m.Title, "slug": m.Slug})
	native, ok := application.Store.(*store.Store)
	if !ok {
		t.Fatalf("createItem needs the native store, not %T", application.Store)
	}
	if err := native.CreateItem(ctx, m, created); err != nil {
		t.Fatal(err)
	}
	if prepared.Description != "" {
		if err := os.WriteFile(filepath.Join(application.Store.ItemDir(m.ID), model.DescriptionFilename), []byte(prepared.Description), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// activateItem moves an item to active, as the coordinator's state command
// does.
func activateItem(t *testing.T, application *app.App, id string) model.Manifest {
	t.Helper()
	m, err := application.Store.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	m.State, m.StateChangedAt = model.StateActive, application.Clock.Now()
	if err := application.Store.SaveManifest(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m
}

// startItem creates an active item with a claimed checkout and an agent
// runtime in mode, as the daemon's start does.
func startItem(t *testing.T, application *app.App, opts app.NewWorkItemOptions, mode agent.Mode) (model.Manifest, app.EnsureAgentRuntimeResult) {
	t.Helper()
	m := createItem(t, application, opts)
	activateItem(t, application, m.ID)
	resolve := app.ResolveOptions{Selector: m.ID}
	if _, err := application.EnsureWorkItemWorkspace(context.Background(), resolve); err != nil {
		t.Fatal(err)
	}
	runtime, err := application.EnsureAgentRuntime(context.Background(), resolve, mode)
	if err != nil {
		t.Fatal(err)
	}
	m, err = application.Store.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m, runtime
}

// liveRuntime records runtime as the item's running agent, with host process
// 4321 (which the test's fake process inspector must report alive), and
// listens on its control socket.
func liveRuntime(t *testing.T, st *store.Store, runtime model.AgentRuntime) (model.AgentRuntime, *agent.ControlSocketServer) {
	t.Helper()
	runtime.HostPID, runtime.HostProcessGroup, runtime.HostStartTime = 4321, 4321, 4321
	if err := st.SaveAgentRuntime(context.Background(), runtime.WorkItemID, runtime); err != nil {
		t.Fatal(err)
	}
	control, err := agent.ListenControlSocket(filepath.Join(st.ItemDir(runtime.WorkItemID), "agent", "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Close() })
	return runtime, control
}

// navigationStore counts full event-history reads, which attention commands
// must not make.
type navigationStore struct {
	app.Store
	historyReads int
}

func (s *navigationStore) ReadEvents(string) ([]model.Event, error) {
	s.historyReads++
	return nil, fmt.Errorf("attention commands must not fetch the full event history")
}

type fakeConversationBackend struct{}

func (fakeConversationBackend) ValidateConfig(context.Context, string, json.RawMessage) error {
	return nil
}

func (fakeConversationBackend) NewConversation(_ context.Context, id, _ string, _ json.RawMessage) (model.Conversation, error) {
	return piadapter.New("pi").NewConversation(id), nil
}
func (fakeConversationBackend) ConversationCWD(context.Context, string) (string, error) {
	return "", nil
}
func (fakeConversationBackend) ForkConversation(context.Context, string, string, string) error {
	return nil
}
func (fakeConversationBackend) ObserveConversation(context.Context, model.Conversation, string, time.Time) (driverprotocol.ConversationObservation, []string, error) {
	return driverprotocol.ConversationObservation{}, nil, nil
}

type fakeGit struct {
	info          model.RepositoryInfo
	head          string
	status        string
	currentBranch string
	branches      map[string]bool
	worktrees     map[string]string
	commits       []string
}

func newFakeGit(t *testing.T) *fakeGit {
	t.Helper()
	return &fakeGit{info: repoInfo(t), branches: map[string]bool{}, worktrees: map[string]string{}}
}

func (f *fakeGit) DetectRepository(ctx context.Context, dir, revision string) (model.RepositoryInfo, error) {
	return f.info, nil
}
func (f *fakeGit) DefaultBranch(context.Context, string) (string, error) { return "main", nil }
func (f *fakeGit) Head(ctx context.Context, dir string) (string, error) {
	if f.head != "" {
		return f.head, nil
	}
	return f.info.Commit, nil
}
func (f *fakeGit) UntrackedFiles(context.Context, string) ([]string, error) { return nil, nil }
func (f *fakeGit) ListFiles(context.Context, string) ([]string, error)      { return nil, nil }
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
func (f *fakeGit) CommitAll(ctx context.Context, dir, message string) (string, error) {
	f.commits = append(f.commits, dir)
	f.status = ""
	return "c0ffee0123456789c0ffee0123456789c0ffee01", nil
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
func (f *fakeGit) DeleteBranch(ctx context.Context, repoRoot, branch string, force bool) error {
	delete(f.branches, branch)
	return nil
}

type fakeTmux struct {
	current           string
	env               map[string]string
	sessionEnvs       map[string]map[string]string
	sessions          map[string]bool
	ensured           []tmuxpkg.SessionSpec
	launched          []tmuxpkg.LaunchSpec
	killed            []string
	killErr           error
	panePath          string
	panePaths         map[string]string
	panePathResponses []string
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
	return nil
}
func (f *fakeTmux) PaneInfo(ctx context.Context, target string) (tmuxpkg.PaneInfo, error) {
	path := f.panePath
	responseSet := len(f.panePathResponses) > 0
	if responseSet {
		path = f.panePathResponses[0]
		f.panePathResponses = f.panePathResponses[1:]
	}
	if path == "" && !responseSet {
		path = f.panePaths[target]
	}
	if path == "" && !responseSet {
		path = "/tmp"
	}
	return tmuxpkg.PaneInfo{SessionName: "wi-test", WindowName: "agent", PaneID: "%1", PaneIndex: "0", PanePID: 123, Command: "wi", CurrentPath: path}, nil
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
	mu         sync.RWMutex
	alive      map[int]bool
	descendant processpkg.Info
	found      bool
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
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.descendant, f.found, nil
}

type fakePi struct {
	sessionPath string
	cwd         string
	env         map[string]string
	mode        agent.Mode
	logPath     string
	controlPath string
	eventPath   string
}

func (f *fakePi) ValidateConfig(context.Context, string, json.RawMessage) error { return nil }

func (f *fakePi) NewConversation(_ context.Context, id, _ string, _ json.RawMessage) (model.Conversation, error) {
	return piadapter.New("pi").NewConversation(id), nil
}
func (f *fakePi) ConversationCWD(_ context.Context, path string) (string, error) {
	return piadapter.New("pi").ConversationCWD(path)
}
func (f *fakePi) ObserveConversation(_ context.Context, conversation model.Conversation, path string, now time.Time) (driverprotocol.ConversationObservation, []string, error) {
	status, warnings := piadapter.ObserveConversationPath(path, now)
	return status, warnings, nil
}
func (f *fakePi) ForkConversation(ctx context.Context, sourcePath, targetPath, targetCWD string) error {
	return piadapter.New("pi").ForkConversation(ctx, sourcePath, targetPath, targetCWD)
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

func envValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func repoInfo(t *testing.T) model.RepositoryInfo {
	t.Helper()
	commit := strings.Repeat("a", 40)
	root := t.TempDir()
	return model.RepositoryInfo{
		Repository: model.Repository{
			RootAtCreation:    root,
			GitCommonDir:      root + "/.git",
			RemoteURL:         "git@example.com:acme/repo.git",
			CreatedFromCommit: commit,
		},
		Commit: commit,
	}
}

func (fakeConversationBackend) InvokeModel(context.Context, string, json.RawMessage, driverprotocol.ModelInvokeRequest) (string, error) {
	return "Generated title", nil
}

func (*fakePi) InvokeModel(context.Context, string, json.RawMessage, driverprotocol.ModelInvokeRequest) (string, error) {
	return "Generated title", nil
}
