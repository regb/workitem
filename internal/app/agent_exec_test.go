package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/runtimepath"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

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
		if !strings.HasPrefix(request.AppendSystemPrompt, config.DefaultAppendSystemPrompt) || !strings.Contains(request.AppendSystemPrompt, "wi-message") {
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
	sequenceIDs(t, application, itemID, sessionID)
	started, _ := startItem(t, application, app.NewWorkItemOptions{Title: "Exec"}, agent.ModeTUI)
	if started.PrimaryConversation == nil {
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
	if err := application.AgentExec(context.Background(), itemID, started.PrimaryConversation.ID, runtime.ID, agent.ModeTUI); err != nil {
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
	sequenceIDs(t, application, itemID, sessionID)
	started, _ := startItem(t, application, app.NewWorkItemOptions{Title: "Locked"}, agent.ModeTUI)
	if started.PrimaryConversation == nil {
		t.Fatal("primary conversation missing")
	}
	lockFile, err := os.OpenFile(filepath.Join(st.ItemDir(itemID), "locks", "pi-"+started.PrimaryConversation.ID+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
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
	if err := application.AgentExec(context.Background(), itemID, started.PrimaryConversation.ID, runtime.ID, agent.ModeTUI); err == nil {
		t.Fatal("expected locked session exec to fail")
	}
}
