package app

import (
	"context"
	"strings"
	"testing"
	"time"

	executorprotocol "github.com/regb/workitem/executor/protocol"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/version"
)

func TestRemoteWorkspaceAgentRequiresBundledPiDriver(t *testing.T) {
	manifest := model.Manifest{Agent: "custom", Checkout: model.Checkout{Claim: &model.WorkspaceClaim{Executor: "worker"}}}
	application := &App{AgentsConfig: map[string]config.AgentDefinitionConfig{"custom": {Driver: "custom"}}, DriversConfig: map[string]config.DriverDefinitionConfig{"custom": {Command: "/bin/custom"}}}
	if err := application.validateRemoteWorkspaceAgent(manifest); err == nil {
		t.Fatal("expected unsupported remote workspace driver to fail")
	}
	manifest.Agent = "default"
	application.AgentsConfig = map[string]config.AgentDefinitionConfig{"default": {Driver: "pi"}}
	application.DriversConfig = map[string]config.DriverDefinitionConfig{"pi": {}}
	if err := application.validateRemoteWorkspaceAgent(manifest); err != nil {
		t.Fatal(err)
	}
	application.DriversConfig["pi"] = config.DriverDefinitionConfig{Command: "/external/pi-driver"}
	if err := application.validateRemoteWorkspaceAgent(manifest); err == nil {
		t.Fatal("expected external Pi driver to fail closed")
	}
}

type fakeExecutorClient struct {
	workspaceCalls int
	deleteCalls    int
	operations     []string
}

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
func (f *fakeExecutorClient) ExecuteWorkspaceTool(_ context.Context, request executorprotocol.ToolRequest) (executorprotocol.ToolResult, error) {
	return executorprotocol.ToolResult{}, nil
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
func (*fakeExecutorClient) Shutdown(context.Context) error { return nil }
func (*fakeExecutorClient) TerminalCommands(string) ([]string, []string, error) {
	return []string{"ssh", "worker", "agent"}, []string{"ssh", "worker", "shell"}, nil
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

func TestCloseRemoteTerminalClosesHostTerminal(t *testing.T) {
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
	if !result.Changed || mux.killed != manifest.TerminalSessionName() {
		t.Fatalf("result=%+v killed=%q", result, mux.killed)
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
	if strings.Join(executor.operations, ",") != "release" || result.Checkout.Present() {
		t.Fatalf("operations=%v checkout=%+v", executor.operations, result.Checkout)
	}
}

type existingRemoteGatewayTmux struct {
	fakeBaseTmux
	copyTarget string
}

func (existingRemoteGatewayTmux) HasSession(context.Context, string) (bool, error) { return true, nil }
func (f *existingRemoteGatewayTmux) CopyMode(_ context.Context, target string) error {
	f.copyTarget = target
	return nil
}

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
	mux := &existingRemoteGatewayTmux{}
	application := New(st, nil)
	application.Tmux = mux
	application.ExecutorsConfig = map[string]config.ExecutorConfig{"worker": {Destination: "worker"}}
	application.ExecutorClients = map[string]ExecutorClient{"worker": &fakeExecutorClient{}}
	result, err := application.EnterTerminalCopyMode(context.Background(), ResolveOptions{Selector: manifest.ID}, "%42")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || mux.copyTarget != "%42" {
		t.Fatalf("result=%+v target=%q", result, mux.copyTarget)
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
