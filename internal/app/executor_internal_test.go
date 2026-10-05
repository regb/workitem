package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	executorprotocol "github.com/regb/workitem/executor/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
	"github.com/regb/workitem/internal/version"
)

func TestExecutorAgentConfigKeepsHostLocalPiPathsOnWorker(t *testing.T) {
	application := &App{
		AgentsConfig:    map[string]config.AgentDefinitionConfig{"default": {Driver: "pi", Config: map[string]any{"extensions": []string{"/coordinator/extension.ts"}}}},
		DriversConfig:   map[string]config.DriverDefinitionConfig{"pi": {Defaults: map[string]any{"executable": "/coordinator/pi"}}},
		ExecutorsConfig: map[string]config.ExecutorConfig{"macbook": {Agents: map[string]config.ExecutorAgentConfig{"default": {Config: map[string]any{"executable": "/worker/pi"}}}}},
	}
	content, err := application.executorAgentConfig("macbook", "default")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "/coordinator/") || !strings.Contains(content, `executable = "/worker/pi"`) {
		t.Fatalf("config=%s", content)
	}
}

func TestExecutorAgentConfigUsesWorkerDriverCommand(t *testing.T) {
	application := &App{
		AgentsConfig:    map[string]config.AgentDefinitionConfig{"custom": {Driver: "custom"}},
		DriversConfig:   map[string]config.DriverDefinitionConfig{"custom": {Command: "/coordinator/custom"}},
		ExecutorsConfig: map[string]config.ExecutorConfig{"worker": {Drivers: map[string]config.ExecutorDriverConfig{"custom": {Command: "/worker/custom"}}}},
	}
	content, err := application.executorAgentConfig("worker", "custom")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "/coordinator/custom") || !strings.Contains(content, `command = "/worker/custom"`) {
		t.Fatalf("config=%s", content)
	}
}

type fakeExecutorClient struct {
	workspaceCalls         int
	runtimeCalls           int
	runtimeStatusResult    executorprotocol.RuntimeResult
	terminalCloseCalls     int
	terminalCopyCalls      int
	terminalCopyAttachment string
	deleteCalls            int
	operations             []string
}

func (f *fakeExecutorClient) EnsureAuthority(context.Context, string) error { return nil }
func (f *fakeExecutorClient) ReconcileItems(context.Context, []string) (executorprotocol.ItemsResult, error) {
	return executorprotocol.ItemsResult{}, nil
}
func (f *fakeExecutorClient) DeleteItem(context.Context, string) error {
	f.deleteCalls++
	return nil
}
func (f *fakeExecutorClient) Doctor(context.Context) (executorprotocol.DoctorResult, error) {
	return executorprotocol.DoctorResult{Version: version.Current().Version, Tools: map[string]string{"git": "ok", "tmux": "ok", "pi": "ok", "bash": "ok"}}, nil
}
func (f *fakeExecutorClient) EnsureWorkspace(_ context.Context, manifest model.Manifest, _ string, _ string, slot string, _ string) (executorprotocol.WorkspaceResult, error) {
	f.workspaceCalls++
	return executorprotocol.WorkspaceResult{ClaimID: "claim-test", ClaimGeneration: manifest.Checkout.ClaimGeneration + 1, Executor: manifest.Checkout.Specification.Executor, RepositoryKey: executorRepositoryKey(manifest.Repository), Path: "/worker/slot", Slot: slot, Source: manifest.Repository.CreatedFromCommit}, nil
}
func (f *fakeExecutorClient) EnsureRuntime(_ context.Context, itemID, mode string) (executorprotocol.RuntimeResult, error) {
	f.runtimeCalls++
	conversation := model.Conversation{Driver: "pi", ID: "01KZYHGDCECSFS4BJ2SNTQP49W", Locator: "sessions/pi/test.jsonl", Format: "pi-jsonl"}
	manifest := model.Manifest{PrimaryConversation: &conversation}
	runtime := model.AgentRuntime{ID: "runtime-remote", WorkItemID: itemID, Mode: mode, State: model.AgentRuntimeRunning, ConversationID: conversation.ID, Capabilities: model.RuntimeCapabilities{NativeTUI: true, Prompt: true}}
	manifestJSON, _ := json.Marshal(manifest)
	runtimeJSON, _ := json.Marshal(runtime)
	return executorprotocol.RuntimeResult{ManifestJSON: manifestJSON, RuntimeJSON: runtimeJSON}, nil
}
func (f *fakeExecutorClient) RuntimeStatus(context.Context, string) (executorprotocol.RuntimeResult, error) {
	return f.runtimeStatusResult, nil
}
func (*fakeExecutorClient) RuntimeControl(context.Context, string, string, string) (executorprotocol.RuntimeResult, error) {
	return executorprotocol.RuntimeResult{}, nil
}
func (*fakeExecutorClient) WorkspaceStatus(context.Context, string) (executorprotocol.WorkspaceResult, error) {
	return executorprotocol.WorkspaceResult{StatusJSON: []byte(`{}`)}, nil
}
func (f *fakeExecutorClient) ReleaseWorkspace(context.Context, model.Manifest, bool) (executorprotocol.WorkspaceResult, error) {
	f.operations = append(f.operations, "release")
	return executorprotocol.WorkspaceResult{}, nil
}
func (*fakeExecutorClient) ImportWorkspaceBundle(context.Context, model.Manifest, []byte) error {
	return nil
}
func (f *fakeExecutorClient) TerminalCopyMode(_ context.Context, _, attachmentID string) error {
	f.terminalCopyCalls++
	f.terminalCopyAttachment = attachmentID
	return nil
}
func (f *fakeExecutorClient) CloseTerminal(context.Context, string) error {
	f.terminalCloseCalls++
	f.operations = append(f.operations, "close")
	return nil
}
func (*fakeExecutorClient) Shutdown(context.Context) error { return nil }
func (*fakeExecutorClient) TerminalCommands(string) ([]string, []string, error) {
	return []string{"ssh", "worker", "agent"}, []string{"ssh", "worker", "shell"}, nil
}

type fakeRemoteAccessTmux struct {
	fakeBaseTmux
	spec           tmuxpkg.SessionSpec
	command        []string
	windowCommands map[string][]string
	defaultCommand string
	client         string
	inTmux         bool
}

func (f *fakeRemoteAccessTmux) EnsureGatewaySession(_ context.Context, spec tmuxpkg.SessionSpec, agent, shell []string) (bool, error) {
	f.spec = spec
	f.command = append([]string(nil), agent...)
	f.windowCommands = map[string][]string{"agent": append([]string(nil), agent...), "shell": append([]string(nil), shell...)}
	f.defaultCommand, _ = tmuxpkg.ShellCommand(shell)
	return true, nil
}
func (f *fakeRemoteAccessTmux) AttachOrSwitchClient(_ context.Context, _ string, inTmux bool, client string) error {
	f.inTmux, f.client = inTmux, client
	return nil
}

func TestRemoteTerminalAccessUsesLocalGatewaySession(t *testing.T) {
	executor := &fakeExecutorClient{}
	mux := &fakeRemoteAccessTmux{}
	home := t.TempDir()
	application := &App{Tmux: mux, Home: home, Environment: map[string]string{"PATH": "/bin"}, ExecutorsConfig: map[string]config.ExecutorConfig{"worker": {}}, ExecutorClients: map[string]ExecutorClient{"worker": executor}}
	manifest := model.Manifest{ID: "01KZYHGDCECSFS4BJ2SNTQP49V", Checkout: model.NewCheckout(model.WorkspaceKindManagedSlot, "worker", "")}
	result, err := application.ensureRemoteTerminalAccess(context.Background(), ResolveOptions{Env: map[string]string{"TMUX": "/tmp/tmux", "WI_TMUX_CLIENT": "client-7"}}, manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || !result.Attached || !mux.inTmux || mux.client != "client-7" || strings.Join(mux.command, " ") != "ssh worker agent" {
		t.Fatalf("result=%+v client=%q command=%v", result, mux.client, mux.command)
	}
	if mux.spec.CWD != home || mux.spec.Env["WI_ID"] != manifest.ID || mux.spec.Env["WI_EXECUTOR"] != "worker" || mux.spec.Env["PATH"] != "/bin" {
		t.Fatalf("spec=%+v", mux.spec)
	}
	if strings.Join(mux.windowCommands["shell"], " ") != "ssh worker shell" || mux.defaultCommand != "ssh worker shell" {
		t.Fatalf("windows=%v default=%q", mux.windowCommands, mux.defaultCommand)
	}
}

func TestRemoteTerminalAccessAttachesLocalGatewayOutsideTmux(t *testing.T) {
	executor := &fakeExecutorClient{}
	mux := &fakeRemoteAccessTmux{}
	application := &App{Tmux: mux, Home: t.TempDir(), ExecutorsConfig: map[string]config.ExecutorConfig{"worker": {}}, ExecutorClients: map[string]ExecutorClient{"worker": executor}}
	manifest := model.Manifest{ID: "01KZYHGDCECSFS4BJ2SNTQP49V", Checkout: model.NewCheckout(model.WorkspaceKindManagedSlot, "worker", "")}
	result, err := application.ensureRemoteTerminalAccess(context.Background(), ResolveOptions{}, manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Attached || mux.inTmux {
		t.Fatalf("result=%+v inTmux=%t", result, mux.inTmux)
	}
}

func TestDeleteRemoteItemDeletesWorkerReplicaFirst(t *testing.T) {
	st := store.New(t.TempDir())
	manifest := model.NewManifest("01KZYHGDCECSFS4BJ2SNTQP49V", "", "Remote", nil, false, time.Now(), model.Repository{}, model.NewCheckout(model.WorkspaceKindManagedSlot, "worker", "wi/remote"))
	manifest.State = model.StateArchived
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutorClient{}
	application := New(st, nil)
	application.Tmux = fakeBaseTmux{}
	application.ExecutorsConfig = map[string]config.ExecutorConfig{"worker": {}}
	application.ExecutorClients = map[string]ExecutorClient{"worker": executor}
	result, err := application.DeleteWorkItems(context.Background(), DeleteWorkItemsOptions{ResolveOptions: ResolveOptions{Selector: manifest.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || executor.deleteCalls != 1 {
		t.Fatalf("result=%+v delete calls=%d", result, executor.deleteCalls)
	}
}

type closingRemoteGatewayTmux struct {
	fakeBaseTmux
	killed string
}

func (*closingRemoteGatewayTmux) HasSession(context.Context, string) (bool, error) { return true, nil }
func (f *closingRemoteGatewayTmux) KillSession(_ context.Context, session string) error {
	f.killed = session
	return nil
}

func TestCloseRemoteTerminalClosesWorkerAndLocalGateway(t *testing.T) {
	st := store.New(t.TempDir())
	path := "/worker/slot"
	manifest := model.NewManifest("01KZYHGDCECSFS4BJ2SNTQP49V", "remote", "Remote", nil, false, time.Now(), model.Repository{}, model.Checkout{Specification: model.CheckoutSpecification{Kind: model.WorkspaceKindManagedSlot, Executor: "worker"}, Claim: &model.WorkspaceClaim{ID: "test-claim", Executor: "worker", Path: path}})
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutorClient{}
	mux := &closingRemoteGatewayTmux{}
	application := New(st, nil)
	application.Tmux = mux
	application.ExecutorsConfig = map[string]config.ExecutorConfig{"worker": {}}
	application.ExecutorClients = map[string]ExecutorClient{"worker": executor}
	result, err := application.CloseTerminal(context.Background(), ResolveOptions{Selector: manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || executor.terminalCloseCalls != 1 || mux.killed != manifest.TerminalSessionName() {
		t.Fatalf("result=%+v closes=%d killed=%q", result, executor.terminalCloseCalls, mux.killed)
	}
}

func TestLifecycleCleanupClosesRemoteTerminalBeforeReleasingClaim(t *testing.T) {
	st := store.New(t.TempDir())
	checkout := model.NewCheckout(model.WorkspaceKindManagedSlot, "worker", "wi/remote")
	checkout.Claim = &model.WorkspaceClaim{ID: "claim-1", Executor: "worker", RepositoryKey: "repo", Slot: "slot-0001", Path: "/worker/slot"}
	manifest := model.NewManifest("01KZYHGDCECSFS4BJ2SNTQP49V", "remote", "Remote", nil, false, time.Now(), model.Repository{}, checkout)
	manifest.State = model.StateWorking
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutorClient{}
	application := New(st, nil)
	application.Tmux = fakeBaseTmux{}
	application.ExecutorsConfig = map[string]config.ExecutorConfig{"worker": {}}
	application.ExecutorClients = map[string]ExecutorClient{"worker": executor}
	result, _, _, err := application.closeWorkspace(context.Background(), manifest, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(executor.operations, ",") != "close,release" || result.Checkout.Present() {
		t.Fatalf("operations=%v checkout=%+v", executor.operations, result.Checkout)
	}
}

type existingRemoteGatewayTmux struct{ fakeBaseTmux }

func (existingRemoteGatewayTmux) HasSession(context.Context, string) (bool, error) { return true, nil }

func TestRemoteTerminalCopyModeUsesExecutorSession(t *testing.T) {
	st := store.New(t.TempDir())
	checkout := model.NewCheckout(model.WorkspaceKindManagedSlot, "worker", "wi/remote")
	if _, err := checkout.AssignNewClaim("worker", "repo", "slot-0001", "/worker/slot"); err != nil {
		t.Fatal(err)
	}
	manifest := model.NewManifest("01KZYHGDCECSFS4BJ2SNTQP49V", "remote", "Remote", nil, false, time.Now(), model.Repository{}, checkout)
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	client := &fakeExecutorClient{}
	application := New(st, nil)
	application.ExecutorsConfig = map[string]config.ExecutorConfig{"worker": {Destination: "worker"}}
	application.ExecutorClients = map[string]ExecutorClient{"worker": client}
	result, err := application.EnterTerminalCopyMode(context.Background(), ResolveOptions{Selector: manifest.ID}, "%42")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || client.terminalCopyCalls != 1 || client.terminalCopyAttachment != "%42" {
		t.Fatalf("result=%+v calls=%d attachment=%q", result, client.terminalCopyCalls, client.terminalCopyAttachment)
	}
}

func TestRemoteTerminalStatusUsesLocalGateway(t *testing.T) {
	st := store.New(t.TempDir())
	manifest := model.NewManifest("01KZYHGDCECSFS4BJ2SNTQP49V", "remote", "Remote", nil, false, time.Now(), model.Repository{}, model.NewCheckout(model.WorkspaceKindManagedSlot, "worker", ""))
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	application := New(st, nil)
	application.Tmux = existingRemoteGatewayTmux{}
	status, err := application.TerminalStatus(context.Background(), ResolveOptions{Selector: manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Inspected || !status.Exists || status.Current {
		t.Fatalf("status=%+v", status)
	}
}

func TestSelectExecutorOnlyBeforeFirstMaterialization(t *testing.T) {
	st := store.New(t.TempDir())
	checkout := model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "wi/remote")
	if _, err := checkout.AssignNewClaim(model.ExecutorLocal, "repo", "slot-0001", "/workspace"); err != nil {
		t.Fatal(err)
	}
	checkout.ClearClaim()
	manifest := model.NewManifest("01KZYHGDCECSFS4BJ2SNTQP49V", "remote", "Remote", nil, false, time.Now(), model.Repository{RootAtCreation: t.TempDir(), CreatedFromCommit: strings.Repeat("a", 40)}, checkout)
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	application := New(st, nil)
	application.ExecutorsConfig = map[string]config.ExecutorConfig{"worker": {Destination: "worker"}}
	if _, err := application.SelectExecutor(context.Background(), ResolveOptions{Selector: manifest.ID}, "worker"); err == nil || !strings.Contains(err.Error(), "fixed after the first workspace materialization") {
		t.Fatalf("err=%v", err)
	}
}

func TestSelectExecutorHonorsPreGenerationMaterializationHistory(t *testing.T) {
	st := store.New(t.TempDir())
	manifest := model.NewManifest("01KZYHGDCECSFS4BJ2SNTQP49V", "remote", "Remote", nil, false, time.Now(), model.Repository{RootAtCreation: t.TempDir(), CreatedFromCommit: strings.Repeat("a", 40)}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "wi/remote"))
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent(context.Background(), manifest.ID, model.NewEvent(time.Now(), "checkout.assigned", "wi", nil)); err != nil {
		t.Fatal(err)
	}
	application := New(st, nil)
	application.ExecutorsConfig = map[string]config.ExecutorConfig{"worker": {Destination: "worker"}}
	if _, err := application.SelectExecutor(context.Background(), ResolveOptions{Selector: manifest.ID}, "worker"); err == nil || !strings.Contains(err.Error(), "fixed after the first workspace materialization") {
		t.Fatalf("err=%v", err)
	}
}

func TestSelectExecutorBeforeMaterialization(t *testing.T) {
	st := store.New(t.TempDir())
	manifest := model.NewManifest("01KZYHGDCECSFS4BJ2SNTQP49V", "remote", "Remote", nil, false, time.Now(), model.Repository{RootAtCreation: t.TempDir(), CreatedFromCommit: strings.Repeat("a", 40)}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "wi/remote"))
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	application := New(st, nil)
	application.ExecutorsConfig = map[string]config.ExecutorConfig{"worker": {Destination: "worker"}}
	result, err := application.SelectExecutor(context.Background(), ResolveOptions{Selector: manifest.ID}, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Manifest.Checkout.Specification.Executor != "worker" || result.Manifest.Checkout.EverMaterialized() {
		t.Fatalf("result=%+v", result)
	}
}

func TestEnsureAgentRuntimeUsesSelectedExecutor(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Ensure(); err != nil {
		t.Fatal(err)
	}
	checkout := model.NewCheckout(model.WorkspaceKindManagedSlot, "worker", "wi/remote")
	manifest := model.NewManifest("01KZYHGDCECSFS4BJ2SNTQP49V", "remote", "Remote", nil, false, time.Now(), model.Repository{RootAtCreation: t.TempDir(), GitCommonDir: t.TempDir(), CreatedFromCommit: strings.Repeat("a", 40)}, checkout)
	manifest.State = model.StateWorking
	if err := st.CreateItem(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	client := &fakeExecutorClient{}
	application := New(st, nil)
	application.ExecutorsConfig = map[string]config.ExecutorConfig{"worker": {Destination: "worker"}}
	application.ExecutorClients = map[string]ExecutorClient{"worker": client}
	result, err := application.EnsureAgentRuntime(context.Background(), ResolveOptions{Selector: manifest.ID}, agent.ModeTUI)
	if err != nil {
		t.Fatal(err)
	}
	if client.workspaceCalls != 1 || client.runtimeCalls != 1 || result.Runtime.Executor != "worker" || result.Conversation.Executor != "worker" || result.Terminal == nil {
		t.Fatalf("calls=%d/%d result=%+v", client.workspaceCalls, client.runtimeCalls, result)
	}
	if result.Capabilities != (agent.Capabilities{NativeTUI: true, Prompt: true}) {
		t.Fatalf("capabilities=%+v", result.Capabilities)
	}
}
