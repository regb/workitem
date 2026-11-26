package app_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestTUIRuntimeLaunchesPrimaryPiSession(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 16)
	sessionID := testutil.ID(t, 17)
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := newTestApp(st, fg)
	application.Tmux = ft
	application.SelfPath = "/usr/local/bin/wi"
	application.Clock = fixedClock{testutil.Time()}
	sequenceIDs(t, application, itemID, sessionID)
	started, _ := startItem(t, application, app.NewWorkItemOptions{Title: "Pi"}, agent.ModeTUI)
	if started.PrimaryConversation == nil {
		t.Fatal("primary conversation missing")
	}
	s := *started.PrimaryConversation
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

func TestTUIRuntimeRecreatesTerminalAndResumesPrimaryConversation(t *testing.T) {
	st := store.New(t.TempDir())
	itemID := testutil.ID(t, 25)
	sessionID := testutil.ID(t, 26)
	fg := newFakeGit(t)
	ft := &fakeTmux{}
	application := newTestApp(st, fg)
	application.Tmux = ft
	application.SelfPath = "wi"
	application.Clock = fixedClock{testutil.Time()}
	sequenceIDs(t, application, itemID, sessionID)
	startItem(t, application, app.NewWorkItemOptions{Title: "Start Resume"}, agent.ModeTUI)
	// The first runtime is gone; the terminal is recreated and the new
	// runtime resumes the same conversation.
	ft.launched = nil
	started, err := application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: itemID}, agent.ModeTUI)
	if err != nil {
		t.Fatal(err)
	}
	if started.Terminal == nil || !started.Terminal.Created || !started.Created || started.Conversation.ID != sessionID {
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
	sequenceIDs(t, application, itemID, sessionID)
	_, ensured := startItem(t, application, app.NewWorkItemOptions{Title: "Headless"}, agent.ModeHeadless)
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
	if _, err := application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: itemID}, agent.ModeTUI); err == nil || !strings.Contains(err.Error(), "is active in headless mode") {
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
	sequenceIDs(t, application, itemID, sourceSessionID, forkedSessionID)
	manifest, _ := startItem(t, application, app.NewWorkItemOptions{Title: "Fork moved session"}, agent.ModeTUI)
	currentCheckout := manifest.Checkout.Claim.Path
	sourcePath := filepath.Join(st.ItemDir(itemID), filepath.FromSlash(manifest.PrimaryConversation.NativeLocator()))
	oldCheckout := filepath.Join(filepath.Dir(currentCheckout), "slot-0004")
	source := fmt.Sprintf("{\"type\":\"session\",\"version\":3,\"id\":\"11111111-1111-4111-8111-111111111111\",\"timestamp\":\"2026-08-03T07:48:31.948Z\",\"cwd\":%q}\n{\"type\":\"message\",\"id\":\"abcd1234\",\"parentId\":null,\"timestamp\":\"2026-08-03T07:49:00Z\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n", oldCheckout)
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	ensured, err := application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: itemID}, agent.ModeTUI)
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
	sequenceIDs(t, application, itemID, testutil.ID(t, 122))
	started, ensured := startItem(t, application, app.NewWorkItemOptions{Title: "Moved checkout"}, agent.ModeTUI)
	runtime := ensured.Runtime
	runtime.HostPID = 4321
	runtime.HostProcessGroup = 4321
	runtime.HostStartTime = 4321
	runtime.State = model.AgentRuntimeRunning
	if err := st.SaveAgentRuntime(context.Background(), itemID, runtime); err != nil {
		t.Fatal(err)
	}
	ft.panePathResponses = []string{"", started.Checkout.Claim.Path}
	if _, err := application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: itemID}, agent.ModeTUI); err != nil {
		t.Fatalf("transient empty pane path was not retried: %v", err)
	}

	ft.panePath = filepath.Join(t.TempDir(), "slot-0004")
	_, err := application.EnsureAgentRuntime(context.Background(), app.ResolveOptions{Selector: itemID}, agent.ModeTUI)
	if err == nil || !strings.Contains(err.Error(), "now owns checkout") {
		t.Fatalf("error = %v", err)
	}
}
