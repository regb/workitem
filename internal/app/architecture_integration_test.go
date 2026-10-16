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

	"github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	piadapter "github.com/regb/workitem/internal/agentdriver/pi"
	"github.com/regb/workitem/internal/app"
	itemcore "github.com/regb/workitem/internal/app/core/item"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	processpkg "github.com/regb/workitem/internal/process"
	"github.com/regb/workitem/internal/runtimepath"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
)

func newTestApp(st *store.Store, git app.Git) *app.App {
	application := app.New(st, git)
	application.DefaultAgent = "pi"
	application.AgentsConfig["pi"] = config.AgentDefinitionConfig{Driver: "pi", Config: map[string]any{}}
	application.ConversationBackends["pi"] = fakeConversationBackend{}
	application.ConversationProjectionObserver = piadapter.ObserveConversation(st.ItemDir)
	return application
}

func enableFixtureBlockerCommands(application *app.App) {
	if native, ok := application.Store.(*store.Store); ok {
		application.Store = &fixtureBlockerStore{Store: native}
	}
}

// Native-file fixtures use the real coordinator policy in a temporary database.
// Production has no native-file fallback for blocker mutations.
type fixtureBlockerStore struct{ *store.Store }

func (s *fixtureBlockerStore) AddBlockers(ctx context.Context, id string, blocks []model.Blocker, at time.Time) (itemcore.BlockerResult, error) {
	root, err := os.MkdirTemp("", "wi-blocker-fixture-")
	if err != nil {
		return itemcore.BlockerResult{}, err
	}
	defer os.RemoveAll(root)
	db, err := coordinator.OpenDatabase(root)
	if err != nil {
		return itemcore.BlockerResult{}, err
	}
	defer db.Close()
	manifests, errs := s.ListManifests()
	if len(errs) > 0 {
		return itemcore.BlockerResult{}, errs[0]
	}
	imported := map[string]coordinator.ImportedManifest{}
	for _, m := range manifests {
		imported[m.ID] = coordinator.ImportedManifest{Manifest: m, Digest: m.ID}
	}
	if _, err := db.SyncImportedManifests(imported, true); err != nil {
		return itemcore.BlockerResult{}, err
	}
	before, err := db.CanonicalManifest(id)
	if err != nil {
		return itemcore.BlockerResult{}, err
	}
	result, err := db.ExecuteManifestCommand(coordinator.ManifestCommand{ID: "fixture-blocker", ProtocolVersion: coordinator.ProtocolVersion, Type: coordinator.CommandBlockerAdd, ItemID: id, ExpectedRevision: &before.Revision, Blockers: blocks, CreatedAt: at})
	if err != nil {
		return itemcore.BlockerResult{}, err
	}
	if err := s.SaveManifest(ctx, result.Manifest); err != nil {
		return itemcore.BlockerResult{}, err
	}
	events, err := db.ListItemEvents(id)
	if err != nil {
		return itemcore.BlockerResult{}, err
	}
	for _, e := range events {
		if e.CausationID != "fixture-blocker" {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(e.Payload, &data); err != nil {
			return itemcore.BlockerResult{}, err
		}
		if err := s.AppendEvent(ctx, id, model.NewEvent(e.Timestamp, e.Type, e.Actor, data)); err != nil {
			return itemcore.BlockerResult{}, err
		}
	}
	return itemcore.BlockerMutationResult(before.Manifest, result.Manifest, result.Changed, at), nil
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
	attach            []string
	killed            []string
	killedAsync       []string
	ensureErr         error
	launchErr         error
	killErr           error
	panePath          string
	panePaths         map[string]string
	panePathResponses []string
	onLaunch          func(tmuxpkg.LaunchSpec) error
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
	application := newTestApp(st, fg)
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
	if started.State != model.StateActive || !started.Changed {
		t.Fatalf("started = %+v", started)
	}
	if _, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: second.Manifest.ID}, false, false); err == nil {
		t.Fatal("expected deep work capacity refusal")
	}
	enableFixtureBlockerCommands(application)
	waiting, err := application.AddBlockers(context.Background(), app.ResolveOptions{Selector: first.Manifest.ID}, []model.Blocker{{Kind: model.BlockerExternal, Event: "review"}})
	if err != nil {
		t.Fatal(err)
	}
	if !waiting.Waiting || waiting.Manifest.State != model.StateActive {
		t.Fatalf("blocked = %+v", waiting)
	}
	resumed, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: first.Manifest.ID}, model.StateActive, false)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != model.StateActive {
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
	if forced.State != model.StateActive {
		t.Fatalf("forced = %+v", forced)
	}
}

func TestResumeWorkItemAttachesWorkspace(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := newTestApp(st, fg)
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
	enableFixtureBlockerCommands(application)
	if _, err := application.AddBlockers(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, []model.Blocker{{Kind: model.BlockerExternal, Event: "review"}}); err != nil {
		t.Fatal(err)
	}
	ft.attach = nil
	res, err := application.ResumeWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Transition.State != model.StateActive || res.Workspace.Terminal.Session == "" || !res.Workspace.Terminal.Attached {
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
	application := newTestApp(st, fg)
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
	if m.State != model.StateActive || !m.Checkout.Present() || m.Checkout.Claim == nil {
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
	application := newTestApp(st, fg)
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
	if m.State != model.StateActive || !m.Checkout.Present() || m.Checkout.Claim == nil {
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
	application := newTestApp(st, fg)
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
	if m.State != model.StateBacklog || !m.Checkout.Present() || m.Checkout.Claim == nil {
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
	application := newTestApp(st, fg)
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
	if _, err := application.Close(context.Background(), app.ResolveOptions{Selector: d.Manifest.ID}, model.ResolutionCompleted, false); err != nil {
		t.Fatal(err)
	}
	list := application.WorkList(app.WorkListOptions{})
	if len(list.Sections.Active) != 1 || list.Sections.Active[0].ID != a.Manifest.ID {
		t.Fatalf("active = %+v", list.Sections.Active)
	}
	if len(list.Sections.Backlog) != 2 || list.Sections.Backlog[0].ID != b.Manifest.ID || list.Sections.Backlog[1].ID != c.Manifest.ID || !list.Sections.Backlog[1].CapacityFull {
		t.Fatalf("backlog = %+v", list.Sections.Backlog)
	}
	archived := application.WorkList(app.WorkListOptions{ArchivedOnly: true})
	if len(archived.Sections.Closed) != 1 || archived.Sections.Closed[0].ID != d.Manifest.ID {
		t.Fatalf("closed = %+v", archived.Sections.Closed)
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
	application := newTestApp(st, newFakeGit(t))
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
	application := newTestApp(st, fg)
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
	firstBranch := first.Manifest.Checkout.Specification.Branch
	archived, err := application.Close(context.Background(), app.ResolveOptions{Selector: first.Manifest.Slug}, model.ResolutionCompleted, false)
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
	if second.Manifest.Checkout.Specification.Branch == firstBranch {
		t.Fatalf("reused slug produced duplicate branch %q", firstBranch)
	}
	restored, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: first.Manifest.ID}, model.StateBacklog, false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Manifest.Slug != "reusable-slug-2" {
		t.Fatalf("restored slug = %q", restored.Manifest.Slug)
	}
	if restored.Manifest.Checkout.Specification.Branch != firstBranch {
		t.Fatalf("restoring item renamed branch from %q to %q", firstBranch, restored.Manifest.Checkout.Specification.Branch)
	}
}

func TestCheckoutCreateRejectsArchivedItem(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := newTestApp(st, fg)
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return testutil.ID(t, 60), nil }
	newRes, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Archived Checkout", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Close(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, model.ResolutionCompleted, false); err != nil {
		t.Fatal(err)
	}
	if _, err := application.CheckoutCreate(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}); err == nil || !strings.Contains(err.Error(), "is closed") {
		t.Fatalf("expected archived checkout refusal, got %v", err)
	}
}

func TestCheckoutStatusReportsBranchMismatch(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := newTestApp(st, fg)
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
	if !res.BranchMismatch || res.CurrentBranch != "main" || res.ExpectedBranch != newRes.Manifest.Checkout.Specification.Branch {
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
	application := newTestApp(st, fg)
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
	if _, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, model.StateActive, false); err != nil {
		t.Fatal(err)
	}
	switched, err := application.SwitchWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if switched.Manifest.State != model.StateActive || !switched.Checkout.Present() || switched.Manifest.PrimaryConversation == nil {
		t.Fatalf("switch porcelain = %+v", switched)
	}
	persisted, err := st.LoadManifest(created.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != model.StateActive {
		t.Fatalf("switch changed state to %s", persisted.State)
	}
}

func TestSwitchWorkItemDoesNotChangeWaitingState(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := newTestApp(st, fg)
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
	enableFixtureBlockerCommands(application)
	waited, err := application.AddBlockers(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, []model.Blocker{{Kind: model.BlockerExternal, Event: "review"}})
	if err != nil {
		t.Fatal(err)
	}
	if !waited.Waiting {
		t.Fatalf("blocked = %+v", waited)
	}
	switched, err := application.SwitchWorkItem(context.Background(), app.ResolveOptions{Selector: newRes.Manifest.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if switched.Manifest.State != model.StateActive || len(switched.Manifest.Blockers) != 1 {
		t.Fatalf("switch changed state or blockers: %+v", switched.Manifest)
	}
	loaded, err := st.LoadManifest(newRes.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != model.StateActive || len(loaded.Blockers) != 1 {
		t.Fatalf("loaded state = %s blockers = %+v", loaded.State, loaded.Blockers)
	}
}

func TestSwitchWorkItemRecyclesOrphanedTUIRuntimeWhenTerminalIsGone(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{sessions: map[string]bool{}}
	fp := &fakeProcess{alive: map[int]bool{}}
	application := newTestApp(st, fg)
	application.Tmux = ft
	application.Process = fp
	application.ConversationBackends["pi"] = &fakePi{}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 80), testutil.ID(t, 81)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Orphaned Runtime", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	itemID := started.Workspace.Manifest.ID
	session := started.Workspace.Manifest.TerminalSessionName()
	conversationID := started.Workspace.Manifest.PrimaryConversation.ID
	runtime := model.AgentRuntime{ID: "tui-orphan", WorkItemID: itemID, Mode: "tui", State: model.AgentRuntimeRunning, HostPID: 444, HostProcessGroup: 444, HostStartTime: 444, ConversationID: conversationID, StartedAt: testutil.Time(), UpdatedAt: testutil.Time()}
	if err := st.SaveAgentRuntime(context.Background(), itemID, runtime); err != nil {
		t.Fatal(err)
	}
	fp.setAlive(444, true)
	// Simulate a tmux server restart: the wi-owned session vanishes while the
	// runtime process keeps running, orphaned.
	delete(ft.sessions, session)

	switched, err := application.SwitchWorkItem(context.Background(), app.ResolveOptions{Selector: itemID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if fp.Alive(444) {
		t.Fatal("orphaned runtime process remained alive")
	}
	if switched.AgentRuntime == nil || switched.AgentRuntime.ID == "tui-orphan" {
		t.Fatalf("switch did not start a fresh runtime: %+v", switched.AgentRuntime)
	}
	if switched.AgentRuntime.ConversationID != conversationID {
		t.Fatalf("fresh runtime did not resume the conversation: %+v", switched.AgentRuntime)
	}
	if !ft.sessions[session] {
		t.Fatal("terminal was not recreated")
	}
	if joined := strings.Join(switched.Warnings, "\n"); !strings.Contains(joined, "was gone") {
		t.Fatalf("expected recycle warning: %+v", switched.Warnings)
	}
}

func TestSwitchWorkItemWarnsWhenAlreadyInTargetTmuxSession(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := newTestApp(st, fg)
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
	application := newTestApp(st, fg)
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
	application := newTestApp(st, fg)
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
	if !workspace.Checkout.Present() || len(ft.ensured) != 0 || workspace.Manifest.PrimaryConversation != nil {
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
	if !terminal.Created || len(ft.ensured) != 1 || afterTerminal.PrimaryConversation != nil {
		t.Fatalf("terminal ensure crossed agent boundary: terminal=%+v manifest=%+v tmux=%+v", terminal, afterTerminal, ft.ensured)
	}
	if _, err := application.ReleaseWorkItemWorkspace(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}); err == nil || !strings.Contains(err.Error(), "terminal session") {
		t.Fatalf("workspace release should require explicit terminal close: %v", err)
	}
	if _, err := application.CloseTerminal(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}); err != nil {
		t.Fatal(err)
	}
	released, err := application.ReleaseWorkItemWorkspace(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID})
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
	application := newTestApp(st, fg)
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
	application := newTestApp(st, fg)
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
	if first.Manifest.Checkout.Specification.Branch != model.ItemBranchName(first.Manifest.Slug, first.Manifest.ID) || second.Manifest.Checkout.Specification.Branch != model.ItemBranchName(second.Manifest.Slug, second.Manifest.ID) {
		t.Fatalf("branches = %q, %q", first.Manifest.Checkout.Specification.Branch, second.Manifest.Checkout.Specification.Branch)
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
	application := newTestApp(st, fg)
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
	if res.Manifest.Checkout.Present() || res.Manifest.Checkout.Claim != nil {
		t.Fatalf("checkout = %+v", res.Manifest.Checkout)
	}
	wantBranch := model.ItemBranchName(res.Manifest.Slug, id)
	if res.Manifest.Checkout.Specification.Branch != wantBranch {
		t.Fatalf("branch = %q", res.Manifest.Checkout.Specification.Branch)
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
	application := newTestApp(st, fg)
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
	if res.Workspace.Manifest.PrimaryConversation == nil {
		t.Fatal("primary conversation missing")
	}
	s := *res.Workspace.Manifest.PrimaryConversation
	if s.ID != sessionID || s.Driver != "pi" || s.NativeLocator() != "sessions/pi/"+sessionID+".jsonl" {
		t.Fatalf("session = %+v", s)
	}
	if len(ft.launched) != 1 || !ft.launched[0].ReuseAgentWindow {
		t.Fatalf("launches = %+v", ft.launched)
	}
	runtime, err := st.LoadAgentRuntime(itemID)
	if err != nil || runtime == nil || runtime.Mode != string(agent.ModeTUI) {
		t.Fatalf("agent runtime = %+v err=%v", runtime, err)
	}
	wantCmd := []string{"/usr/local/bin/wi", "agent", "exec", "--item", itemID, "--conversation", sessionID, "--runtime", runtime.ID, "--mode", "tui"}
	if strings.Join(ft.launched[0].Command, " ") != strings.Join(wantCmd, " ") {
		t.Fatalf("command = %+v, want %+v", ft.launched[0].Command, wantCmd)
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
	application := newTestApp(st, fg)
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
	sessionPath := filepath.Join(st.ItemDir(itemID), started.Workspace.Manifest.PrimaryConversation.NativeLocator())
	appendPiJSONL(t, sessionPath, piMessage(now.Add(-time.Minute), "user", []map[string]any{{"type": "text", "text": "please work"}}))
	status, err := application.AgentStatus(context.Background(), app.AgentStatusOptions{ResolveOptions: app.ResolveOptions{Selector: itemID}, StaleAfter: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "busy" || status.Conversation.InferredTurnState != "incomplete" || !status.Process.Online || status.Process.BackendPID != 456 {
		t.Fatalf("status = %+v", status)
	}
}

func TestAgentStatusIgnoresPiSessionMetadataForTurnState(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 76)
	sessionID := testutil.ID(t, 77)
	fg := newFakeGit(t)
	fp := &fakeProcess{alive: map[int]bool{123: true, 456: true}, descendant: processpkg.Info{PID: 456, Command: "pi", Cmdline: []string{"pi"}}, found: true}
	application := newTestApp(st, fg)
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
	sessionPath := filepath.Join(st.ItemDir(itemID), started.Workspace.Manifest.PrimaryConversation.NativeLocator())
	appendPiJSONL(t, sessionPath,
		map[string]any{"type": "session", "timestamp": now.Add(-time.Hour).Format(time.RFC3339Nano)},
		map[string]any{"type": "model_change", "timestamp": now.Add(-time.Hour).Format(time.RFC3339Nano)},
		map[string]any{"type": "thinking_level_change", "timestamp": now.Add(-time.Hour).Format(time.RFC3339Nano)},
	)
	status, err := application.AgentStatus(context.Background(), app.AgentStatusOptions{ResolveOptions: app.ResolveOptions{Selector: itemID}, StaleAfter: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "idle" || status.Conversation.InferredTurnState != "idle" || status.Conversation.LastTurnActivity != nil || status.Conversation.LastEvent == nil || status.Conversation.LastEvent.Type != "thinking_level_change" || status.Conversation.LastActivityAgeSeconds != 0 {
		t.Fatalf("status = %+v", status)
	}
}

func TestAttentionQueueRanksRecentRequestsBeforeDeferredItems(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := newTestApp(st, fg)
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
		items[name] = createdItem{id: created.Manifest.ID, sessionPath: filepath.Join(st.ItemDir(created.Manifest.ID), started.Workspace.Manifest.PrimaryConversation.NativeLocator())}
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
	enableFixtureBlockerCommands(application)
	if _, err := application.AddBlockers(context.Background(), app.ResolveOptions{Selector: items["B"].id}, []model.Blocker{{Kind: model.BlockerExternal, Event: "review"}}); err != nil {
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
	precomputed := app.AttentionQueueResult{Candidates: []app.AttentionCandidate{{Item: app.WorkListItem{ID: items["E"].id, Slug: "e", Title: "E", State: model.StateActive}, Rank: 1}}}
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
		WaitBlockers:   []model.Blocker{{Kind: model.BlockerExternal, Event: "waiting"}},
	}, false)
	if err != nil || next.Waited == nil || !next.Waited.Waiting || next.Selected.Item.Title != "E" || next.CurrentInQueue {
		t.Fatalf("next --wait from F = %+v err=%v", next, err)
	}
	waitingManifest, err := st.LoadManifest(items["F"].id)
	if err != nil || waitingManifest.State != model.StateActive || len(waitingManifest.Blockers) != 1 {
		t.Fatalf("waited manifest = %+v err=%v", waitingManifest, err)
	}
	waitingEvents, err := st.ReadEvents(items["F"].id)
	if err != nil {
		t.Fatal(err)
	}
	blockedEvent := false
	for _, event := range waitingEvents {
		if event.Type == "work_item.state_set" && event.Data["new_state"] == "waiting" {
			t.Fatalf("next --wait changed lifecycle state: %+v", event)
		}
		blockedEvent = blockedEvent || event.Type == "blocker.added"
	}
	if !blockedEvent {
		t.Fatalf("next --wait did not record a blocker: %+v", waitingEvents)
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
	application := newTestApp(st, fg)
	application.Process = proc
	application.Clock = fixedClock{testutil.Time()}
	itemID := testutil.ID(t, 77)
	application.NewID = func() (string, error) { return itemID, nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Archive Runtime", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
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
	archived, err := application.Close(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, model.ResolutionCompleted, false)
	if err != nil {
		t.Fatal(err)
	}
	if archived.State != model.StateClosed || !containsString(archived.Warnings, "requested agent runtime shutdown") {
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
	application := newTestApp(st, fg)
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
		appendPiJSONL(t, filepath.Join(st.ItemDir(item.Manifest.ID), started.Workspace.Manifest.PrimaryConversation.NativeLocator()),
			piMessage(testutil.Time().Add(time.Minute), "user", []map[string]any{{"type": "text", "text": "work"}}),
			piMessage(testutil.Time().Add(time.Minute+time.Second), "assistant", []map[string]any{{"type": "text", "text": "done"}}),
		)
		created = append(created, item)
	}
	result, err := application.NextWorkItem(context.Background(), app.NextWorkItemOptions{
		ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": created[0].Manifest.ID, "TMUX": "/tmp/tmux"}},
		CloseCurrent:   true,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected.Item.ID != created[1].Manifest.ID || result.Closed == nil || result.Closed.WorkItemID != created[0].Manifest.ID {
		t.Fatalf("result = %+v", result)
	}
	archived, err := st.LoadManifest(created[0].Manifest.ID)
	if err != nil || archived.State != model.StateClosed || archived.Slug != "" || archived.Checkout.Present() {
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
	application := newTestApp(st, fg)
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
		appendPiJSONL(t, filepath.Join(st.ItemDir(item.Manifest.ID), started.Workspace.Manifest.PrimaryConversation.NativeLocator()),
			piMessage(testutil.Time().Add(time.Minute), "user", []map[string]any{{"type": "text", "text": "work"}}),
			piMessage(testutil.Time().Add(time.Minute+time.Second), "assistant", []map[string]any{{"type": "text", "text": "done"}}),
		)
		created = append(created, item)
	}
	mux.current = created[0].Manifest.TerminalSessionName()
	partial, err := application.NextWorkItem(context.Background(), app.NextWorkItemOptions{ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": created[0].Manifest.ID, "TMUX": "/tmp/tmux"}}, CloseCurrent: true}, false)
	if err == nil || partial.Workspace.WorkItemID != created[1].Manifest.ID || !strings.Contains(err.Error(), "cannot close current tmux session") {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
	current, loadErr := st.LoadManifest(created[0].Manifest.ID)
	if loadErr != nil || current.State == model.StateClosed {
		t.Fatalf("current=%+v err=%v", current, loadErr)
	}
}

func TestNextWorkItemArchivePreflightFailsBeforeSwitching(t *testing.T) {
	st := store.New(t.TempDir())
	fg := newFakeGit(t)
	application := newTestApp(st, fg)
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
		appendPiJSONL(t, filepath.Join(st.ItemDir(item.Manifest.ID), started.Workspace.Manifest.PrimaryConversation.NativeLocator()),
			piMessage(testutil.Time().Add(time.Minute), "user", []map[string]any{{"type": "text", "text": "work"}}),
			piMessage(testutil.Time().Add(time.Minute+time.Second), "assistant", []map[string]any{{"type": "text", "text": "done"}}),
		)
		created = append(created, item)
	}
	fg.status = " M dirty.go"
	partial, err := application.NextWorkItem(context.Background(), app.NextWorkItemOptions{ResolveOptions: app.ResolveOptions{Env: map[string]string{"WI_ID": created[0].Manifest.ID}}, CloseCurrent: true}, false)
	if err == nil || partial.Workspace.WorkItemID != "" || !strings.Contains(err.Error(), "could not prepare current item "+created[0].Manifest.ID+" for close before switching") {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
	current, loadErr := st.LoadManifest(created[0].Manifest.ID)
	if loadErr != nil || current.State == model.StateClosed {
		t.Fatalf("current=%+v err=%v", current, loadErr)
	}
}

func TestNextWorkItemFailsForEmptyAttentionRing(t *testing.T) {
	application := newTestApp(store.New(t.TempDir()), newFakeGit(t))
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
	application := newTestApp(st, fg)
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
	sessionPath := filepath.Join(st.ItemDir(itemID), started.Workspace.Manifest.PrimaryConversation.NativeLocator())
	appendPiJSONL(t, sessionPath,
		piMessage(now.Add(-2*time.Minute), "user", []map[string]any{{"type": "text", "text": "please implement"}}),
		piMessage(now.Add(-time.Minute), "assistant", []map[string]any{{"type": "text", "text": "Implemented and tested."}}),
	)
	status, err := application.AgentStatus(context.Background(), app.AgentStatusOptions{ResolveOptions: app.ResolveOptions{Selector: itemID}})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "idle" || status.Conversation.InferredTurnState != "idle" || status.Worktree == nil || status.Worktree.Status != "changed" || !status.Worktree.HasChanges {
		t.Fatalf("status = %+v", status)
	}
}

func TestAgentStatusDerivesProblemWhenIncompleteAndOffline(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 39)
	sessionID := testutil.ID(t, 40)
	fg := newFakeGit(t)
	fp := &fakeProcess{alive: map[int]bool{}}
	application := newTestApp(st, fg)
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
	sessionPath := filepath.Join(st.ItemDir(itemID), started.Workspace.Manifest.PrimaryConversation.NativeLocator())
	appendPiJSONL(t, sessionPath, piMessage(now.Add(-time.Hour), "assistant", []map[string]any{{"type": "toolCall"}}))
	status, err := application.AgentStatus(context.Background(), app.AgentStatusOptions{ResolveOptions: app.ResolveOptions{Selector: itemID}, StaleAfter: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "problem" {
		t.Fatalf("status = %+v", status)
	}
}

func TestStartRecreatedTmuxResumesPrimaryConversation(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 25)
	sessionID := testutil.ID(t, 26)
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := newTestApp(st, fg)
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
	if !started.Workspace.Terminal.Created || !started.Workspace.AgentLaunched || started.Workspace.Conversation == nil || started.Workspace.Conversation.ID != sessionID {
		t.Fatalf("start result = %+v", started)
	}
	if len(ft.launched) != 1 || !ft.launched[0].ReuseAgentWindow {
		t.Fatalf("launches = %+v", ft.launched)
	}
}

func TestHeadlessRuntimeUsesNativeControlWithoutTmux(t *testing.T) {
	t.Setenv("PI_SESSION_ID", "parent-session")
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 111)
	sessionID := testutil.ID(t, 112)
	fg := newFakeGit(t)
	ft := &fakeTmux{env: map[string]string{"HOME": "/home/test", "PATH": "/clean/bin"}}
	fp := &fakeProcess{alive: map[int]bool{4321: true}}
	launcher := &fakeRuntimeLauncher{pid: 4321}
	application := newTestApp(st, fg)
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
	if _, err := application.SetWorkItemState(context.Background(), app.ResolveOptions{Selector: itemID}, model.StateActive, false); err != nil {
		t.Fatal(err)
	}
	ensured, err := application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, agent.ModeHeadless)
	if err != nil {
		t.Fatal(err)
	}
	if !ensured.Created || ensured.Runtime.Mode != "headless" || ensured.Runtime.HostPID != 4321 || len(ft.ensured) != 0 || len(ft.launched) != 0 {
		t.Fatalf("runtime = %+v tmux ensured=%d launched=%d", ensured, len(ft.ensured), len(ft.launched))
	}
	if strings.Join(launcher.spec.Args, " ") != strings.Join([]string{"agent", "exec", "--item", itemID, "--conversation", sessionID, "--runtime", ensured.Runtime.ID, "--mode", "headless"}, " ") {
		t.Fatalf("launcher args = %+v", launcher.spec.Args)
	}
	if got := envValue(launcher.spec.Env, "PATH"); got != os.Getenv("PATH") {
		t.Fatalf("runtime PATH = %q", got)
	}
	if got := envValue(launcher.spec.Env, "PI_SESSION_ID"); got != "" {
		t.Fatalf("runtime inherited PI_SESSION_ID %q", got)
	}
	if _, err := application.SwitchWorkItem(context.Background(), app.ResolveOptions{Selector: itemID}, false); err == nil || !strings.Contains(err.Error(), "currently owns the conversation in headless mode") {
		t.Fatalf("expected safe headless-to-TUI handoff refusal, got %v", err)
	}
}

func TestEnsureAgentRuntimeForksConversationAfterSlotReassignment(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 123)
	sourceSessionID := testutil.ID(t, 124)
	forkedSessionID := testutil.ID(t, 125)
	application := newTestApp(st, newFakeGit(t))
	application.Tmux = &fakeTmux{}
	application.ConversationBackends["pi"] = &fakePi{}
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
	currentCheckout := manifest.Checkout.Claim.Path
	sourcePath := filepath.Join(st.ItemDir(itemID), filepath.FromSlash(manifest.PrimaryConversation.NativeLocator()))
	oldCheckout := filepath.Join(filepath.Dir(currentCheckout), "slot-0004")
	source := fmt.Sprintf("{\"type\":\"session\",\"version\":3,\"id\":\"11111111-1111-4111-8111-111111111111\",\"timestamp\":\"2026-08-03T07:48:31.948Z\",\"cwd\":%q}\n{\"type\":\"message\",\"id\":\"abcd1234\",\"parentId\":null,\"timestamp\":\"2026-08-03T07:49:00Z\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n", oldCheckout)
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	ensured, err := application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, agent.ModeTUI)
	if err != nil {
		t.Fatal(err)
	}
	if ensured.Conversation.ID != forkedSessionID || ensured.Conversation.NativeLocator() == manifest.PrimaryConversation.NativeLocator() {
		t.Fatalf("conversation = %+v", ensured.Conversation)
	}
	forkedPath := filepath.Join(st.ItemDir(itemID), filepath.FromSlash(ensured.Conversation.NativeLocator()))
	if cwd, err := application.ConversationBackends["pi"].ConversationCWD(context.Background(), forkedPath); err != nil || cwd != currentCheckout {
		t.Fatalf("forked cwd=%q err=%v", cwd, err)
	}
	data, err := os.ReadFile(forkedPath)
	if err != nil || !strings.Contains(string(data), "abcd1234") || !strings.Contains(string(data), sourcePath) {
		t.Fatalf("forked data=%q err=%v", data, err)
	}
}

func TestEnsureAgentRuntimeRetriesEmptyPanePathAndRejectsDifferentCheckout(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 121)
	application := newTestApp(st, newFakeGit(t))
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
	ft.panePathResponses = []string{"", started.Workspace.Manifest.Checkout.Claim.Path}
	if _, err = application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, agent.ModeTUI); err != nil {
		t.Fatalf("transient empty pane path was not retried: %v", err)
	}

	ft.panePath = filepath.Join(t.TempDir(), "slot-0004")
	_, err = application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID}, agent.ModeTUI)
	if err == nil || !strings.Contains(err.Error(), "now owns checkout") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewAgentWorkItemStartsDetachedHeadlessRuntimeWithSeparateMessage(t *testing.T) {
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
	application := newTestApp(st, fg)
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
		runtime.Capabilities = model.RuntimeCapabilities{Headless: true, Prompt: true, Steering: true, FollowUp: true, Cancel: true, Monitor: true}
		if err := st.SaveAgentRuntime(context.Background(), itemID, *runtime); err != nil {
			return err
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

	// A configured default must not override this explicit message.
	application.AgentConfig.StartupMessage = "Read wi show and follow the acceptance criteria."
	prompt := "Investigate the refresh race and add a regression test."
	result, err := application.NewAgentWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Refresh race", Description: "The refresh operation can race with writes. Add a regression test.", CWD: t.TempDir()}, agent.ModeHeadless, prompt, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if result.Start.Transition.State != model.StateActive || result.Start.Workspace.AgentRuntime == nil || result.Start.Workspace.AgentRuntime.Mode != string(agent.ModeHeadless) || result.Start.Workspace.Terminal != nil {
		t.Fatalf("start result = %+v", result.Start)
	}
	if !result.Control.Submitted || result.Control.Command.Message != prompt || result.Control.Command.Actor != "planner" {
		t.Fatalf("control result = %+v", result.Control)
	}
	shown, err := application.ShowWorkItem(context.Background(), app.ResolveOptions{Selector: itemID})
	if err != nil {
		t.Fatal(err)
	}
	if shown.Description != "The refresh operation can race with writes. Add a regression test." || shown.Manifest.State != model.StateActive {
		t.Fatalf("shown = %+v", shown)
	}
	if launcher.spec.CWD == "" || strings.Join(launcher.spec.Args, " ") == "" {
		t.Fatalf("launcher spec = %+v", launcher.spec)
	}
	// No ID is available for another control request. Resume must only enter
	// the existing conversation, even with a configured startup message.
	if _, err := application.ResumeWorkItemInMode(context.Background(), app.ResolveOptions{Selector: itemID}, false, agent.ModeHeadless); err != nil {
		t.Fatal(err)
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
	application := newTestApp(st, newFakeGit(t))
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
		runtime.Capabilities = model.RuntimeCapabilities{NativeTUI: true, Prompt: true, Steering: true, FollowUp: true, Cancel: true, Monitor: true}
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

	result, err := application.NewAgentWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Detached TUI", Description: "Implement the requested change.", CWD: t.TempDir()}, "", "", "planner")
	if err != nil {
		t.Fatal(err)
	}
	if result.Start.Workspace.AgentRuntime == nil || result.Start.Workspace.AgentRuntime.Mode != string(agent.ModeTUI) || result.Start.Workspace.Terminal == nil || len(ft.launched) != 1 {
		t.Fatalf("start result = %+v launched=%d", result.Start, len(ft.launched))
	}
	if len(ft.attach) != 0 {
		t.Fatalf("unexpected tmux attachment: %+v", ft.attach)
	}
	if !result.Control.Submitted || result.Control.Command.Message != config.DefaultStartupMessage {
		t.Fatalf("control result = %+v", result.Control)
	}
	shown, err := application.ShowWorkItem(context.Background(), app.ResolveOptions{Selector: itemID})
	if err != nil || shown.Description != "Implement the requested change." {
		t.Fatalf("description=%q error=%v", shown.Description, err)
	}
}

func TestAgentExecHoldsLockAndRunsPiWithExplicitRootConversationLocator(t *testing.T) {
	t.Setenv("WI_DAEMON_SOCKET", "/private/daemon.sock")
	t.Setenv("WI_DAEMON_PROTOCOL", "private")
	t.Setenv("WI_BUILD_IDENTITY", "private")
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 21)
	sessionID := testutil.ID(t, 22)
	fg := newFakeGit(t)
	fp := &fakePi{}
	application := newTestApp(st, fg)
	application.Tmux = &fakeTmux{}
	application.ConversationBackends["pi"] = fp
	application.SelfPath = "/test/wi"
	application.BundledDriverCommands["pi"] = app.BundledDriverCommand{Path: application.SelfPath, Args: []string{"internal-driver", "pi"}}
	var driverEnv []string
	application.RunAgentDriver = func(_ context.Context, _ string, _ []string, request driverprotocol.LaunchRequest, environment []string, onReady func(driverprotocol.Ready) error) error {
		driverEnv = append([]string(nil), environment...)
		if request.AppendSystemPrompt != config.DefaultAppendSystemPrompt {
			t.Fatal("missing default system instructions")
		}
		if request.ProjectTrusted {
			t.Fatal("legacy repository received automatic project approval")
		}
		if len(request.Skills) != 1 || request.Skills[0].Name != "wi" || len(request.Skills[0].Files) == 0 {
			t.Fatal("runtime request omitted the bundled wi skill")
		}
		fp.sessionPath = request.Conversation.Locator
		fp.cwd = request.Runtime.CWD
		fp.env = request.Environment
		fp.mode = agent.Mode(request.Runtime.Mode)
		fp.logPath = request.Runtime.LogPath
		fp.controlPath = request.Runtime.ControlSocketPath
		fp.eventPath = request.Runtime.EventSocketPath
		return onReady(driverprotocol.Ready{ProtocolVersion: driverprotocol.Version, Capabilities: driverprotocol.Capabilities{NativeTUI: true, Prompt: true, Steering: true, FollowUp: true, Cancel: true, Monitor: true}})
	}
	application.AgentRuntimeStateRoot = filepath.Join(t.TempDir(), "state")
	application.AgentRuntimeSocketRoot = filepath.Join(testutil.ShortTempDir(t), "runtime")
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
	if res.Workspace.Manifest.PrimaryConversation == nil {
		t.Fatal("primary conversation missing")
	}
	manifest, err := st.LoadManifest(itemID)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := st.LoadAgentRuntime(itemID)
	if err != nil || runtime == nil {
		t.Fatalf("agent runtime = %+v err=%v", runtime, err)
	}
	if err := application.AgentExec(context.Background(), itemID, res.Workspace.Manifest.PrimaryConversation.ID, runtime.ID, agent.ModeTUI); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(st.ItemDir(itemID), "sessions", "pi", sessionID+".jsonl")
	if fp.sessionPath != wantPath {
		t.Fatalf("pi session path = %q, want %q", fp.sessionPath, wantPath)
	}
	if manifest.Checkout.Claim == nil || fp.cwd != manifest.Checkout.Claim.Path {
		t.Fatalf("pi cwd = %q, checkout = %+v", fp.cwd, manifest.Checkout)
	}
	if fp.env["WI_ID"] != itemID || fp.env["WI_DIR"] != st.ItemDir(itemID) {
		t.Fatalf("env = %+v", fp.env)
	}
	if fp.env["WI_AGENT_CONVERSATION_DIR"] != filepath.Join(st.ItemDir(itemID), "sessions", "pi") {
		t.Fatalf("conversation dir env = %q", fp.env["WI_AGENT_CONVERSATION_DIR"])
	}
	if fp.logPath != filepath.Join(application.AgentRuntimeStateRoot, filepath.FromSlash(runtimepath.DiagnosticLog(itemID, runtime.ID))) {
		t.Fatalf("runtime log path = %q", fp.logPath)
	}
	if fp.controlPath != filepath.Join(application.AgentRuntimeSocketRoot, filepath.FromSlash(runtimepath.ControlSocket(itemID, runtime.ID))) {
		t.Fatalf("control socket path = %q", fp.controlPath)
	}
	if fp.eventPath != filepath.Join(application.AgentRuntimeSocketRoot, filepath.FromSlash(runtimepath.EventSocket(itemID, runtime.ID))) {
		t.Fatalf("event socket path = %q", fp.eventPath)
	}
	for _, entry := range driverEnv {
		if strings.HasPrefix(entry, "WI_DAEMON_SOCKET=") || strings.HasPrefix(entry, "WI_DAEMON_PROTOCOL=") || strings.HasPrefix(entry, "WI_BUILD_IDENTITY=") {
			t.Fatalf("driver received private daemon environment: %q", entry)
		}
	}
}

func TestAgentExecRefusesLockedRootSession(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 23)
	sessionID := testutil.ID(t, 24)
	fg := newFakeGit(t)
	application := newTestApp(st, fg)
	application.Tmux = &fakeTmux{}
	application.ConversationBackends["pi"] = &fakePi{}
	application.SelfPath = "/test/wi"
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
	if res.Workspace.Manifest.PrimaryConversation == nil {
		t.Fatal("primary conversation missing")
	}
	lockFile, err := os.OpenFile(filepath.Join(st.ItemDir(itemID), "locks", "pi-"+res.Workspace.Manifest.PrimaryConversation.ID+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
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
	if err := application.AgentExec(context.Background(), itemID, res.Workspace.Manifest.PrimaryConversation.ID, runtime.ID, agent.ModeTUI); err == nil {
		t.Fatal("expected locked session exec to fail")
	}
}

func TestNewWorkItemUsesStableReadableBranch(t *testing.T) {
	st := store.New(t.TempDir())
	id := testutil.ID(t, 8)
	fg := newFakeGit(t)
	application := newTestApp(st, fg)
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return id, nil }
	res, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Fix Race", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := model.ItemBranchName(res.Manifest.Slug, id)
	if res.Manifest.Checkout.Specification.Branch != want {
		t.Fatalf("branch = %q, want %q", res.Manifest.Checkout.Specification.Branch, want)
	}
}

func TestStartWorkItemReturnsTmuxCreationFailure(t *testing.T) {
	st := store.New(t.TempDir())
	id := testutil.ID(t, 15)
	fg := newFakeGit(t)
	application := newTestApp(st, fg)
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
	if loaded.Checkout.Claim == nil {
		t.Fatalf("checkout should have been assigned before tmux failure: %+v", loaded.Checkout)
	}
	if _, err := os.Stat(loaded.Checkout.Claim.Path); err != nil {
		t.Fatalf("slot should remain after start failure, stat err=%v", err)
	}
}

func TestCheckoutRemoveDoesNotArchiveAndCreateReusesBranch(t *testing.T) {
	st := store.New(t.TempDir())
	id := testutil.ID(t, 10)
	fg := newFakeGit(t)
	application := newTestApp(st, fg)
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
	initialPath := initial.Checkout.Claim.Path
	initialClaim := *initial.Checkout.Claim
	if initial.Checkout.Specification.Kind != model.WorkspaceKindManagedSlot {
		t.Fatalf("assigned checkout kind = %q", initial.Checkout.Specification.Kind)
	}
	removed, err := application.CheckoutRemove(context.Background(), app.ResolveOptions{Selector: id})
	if err != nil {
		t.Fatal(err)
	}
	if removed.Checkout.ClaimGeneration != initialClaim.Generation || removed.Checkout.Specification != initial.Checkout.Specification {
		t.Fatalf("release lost checkout intent or generation: %+v", removed.Checkout)
	}
	if removed.Manifest.State == model.StateClosed || removed.Checkout.Present() || removed.Checkout.Claim != nil {
		t.Fatalf("removed = %+v", removed)
	}
	if _, err := os.Stat(initialPath); err != nil {
		t.Fatalf("slot should remain after checkout release: %v", err)
	}
	created, err := application.CheckoutCreate(context.Background(), app.ResolveOptions{Selector: id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(created.Source, "branch "+newRes.Manifest.Checkout.Specification.Branch) {
		t.Fatalf("source = %q", created.Source)
	}
	if !created.Checkout.Present() || created.Checkout.Claim == nil {
		t.Fatalf("created checkout = %+v", created.Checkout)
	}
	if created.Checkout.Claim.Path != initialPath || created.Checkout.Claim.ID == initialClaim.ID || created.Checkout.Claim.Generation != initialClaim.Generation+1 {
		t.Fatalf("reassignment reused claim identity: old=%+v new=%+v", initialClaim, created.Checkout.Claim)
	}
}

func TestCheckoutRemoveRefusesDirty(t *testing.T) {
	st := store.New(t.TempDir())
	id := testutil.ID(t, 11)
	fg := newFakeGit(t)
	application := newTestApp(st, fg)
	application.Clock = fixedClock{testutil.Time()}
	application.NewID = func() (string, error) { return id, nil }
	if _, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Dirty", CWD: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.CheckoutCreate(context.Background(), app.ResolveOptions{Selector: id}); err != nil {
		t.Fatal(err)
	}
	fg.status = " M file.go\n"
	if _, err := application.CheckoutRemove(context.Background(), app.ResolveOptions{Selector: id}); err == nil {
		t.Fatal("expected dirty checkout release to fail")
	}
}

func TestResolveItemFromTmuxEnvironment(t *testing.T) {
	st := store.New(t.TempDir())
	id := testutil.ID(t, 12)
	fg := newFakeGit(t)
	ft := &fakeTmux{current: "wi-session", env: map[string]string{"WI_ID": id}}
	application := newTestApp(st, fg)
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
