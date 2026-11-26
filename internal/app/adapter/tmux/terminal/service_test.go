package terminal

import (
	"context"
	"testing"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
)

type fakeTmux struct {
	exists   bool
	current  string
	err      error
	specSink *tmuxpkg.SessionSpec
}

func (f fakeTmux) HasSession(context.Context, string) (bool, error) { return f.exists, f.err }
func (f fakeTmux) CurrentSession(context.Context) (string, error)   { return f.current, f.err }
func (f fakeTmux) EnsureSession(_ context.Context, spec tmuxpkg.SessionSpec) (bool, error) {
	if f.specSink != nil {
		*f.specSink = spec
	}
	return false, f.err
}
func (f fakeTmux) KillSession(context.Context, string) error { return f.err }

type fakeStore struct{ saved **model.TerminalRuntime }

func (fakeStore) ItemDir(string) string                                      { return "/items/item-1" }
func (fakeStore) LoadAgentRuntime(string) (*model.AgentRuntime, error)       { return nil, nil }
func (fakeStore) LoadTerminalRuntime(string) (*model.TerminalRuntime, error) { return nil, nil }
func (s fakeStore) SaveTerminalRuntime(_ context.Context, _ string, value model.TerminalRuntime) error {
	if s.saved != nil {
		*s.saved = &value
	}
	return nil
}
func (fakeStore) RemoveTerminalRuntime(context.Context, string) error    { return nil }
func (fakeStore) AppendEvent(context.Context, string, model.Event) error { return nil }

func TestEnsureForManifestAppliesEnvironmentBeforeSessionCreation(t *testing.T) {
	path := "/checkout"
	manifest := model.Manifest{ID: "item-1", Repository: model.Repository{RootAtCreation: "/repo"}, Checkout: model.Checkout{Specification: model.CheckoutSpecification{}, ClaimGeneration: 1, Claim: &model.WorkspaceClaim{ID: "test-claim", Generation: 1, Executor: model.ExecutorLocal, Path: path}}}
	workspace := func(context.Context, contract.ResolveOptions, model.Manifest, bool) (model.Manifest, []string, error) {
		return manifest, nil, nil
	}
	var captured tmuxpkg.SessionSpec
	var saved *model.TerminalRuntime
	service := New(fakeStore{saved: &saved}, fakeTmux{specSink: &captured}, nil, workspace, nil, nil)
	_, err := service.EnsureForManifest(context.Background(), contract.ResolveOptions{}, manifest, true, map[string]string{"PROJECT_TOKEN": "loaded", "PATH": "/project/bin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.WorkspaceClaimID != "test-claim" || saved.WorkspaceClaimGeneration != 1 {
		t.Fatalf("terminal handle cache=%+v", saved)
	}
	if captured.Env["PROJECT_TOKEN"] != "loaded" || captured.Env["PATH"] != "/project/bin" || captured.Env["WI_ID"] != "item-1" {
		t.Fatalf("session environment=%+v", captured.Env)
	}
}

type boundTerminalStore struct{ fakeStore }

func (boundTerminalStore) LoadTerminalRuntime(string) (*model.TerminalRuntime, error) {
	return &model.TerminalRuntime{WorkspaceClaimID: "claim-1", WorkspaceClaimGeneration: 1}, nil
}

func TestTerminalRefusesSupersededClaim(t *testing.T) {
	manifest := model.Manifest{ID: "item-1", State: model.StateActive, Checkout: model.Checkout{ClaimGeneration: 2, Claim: &model.WorkspaceClaim{ID: "claim-2", Generation: 2, Path: "/workspace"}}}
	workspace := func(context.Context, contract.ResolveOptions, model.Manifest, bool) (model.Manifest, []string, error) {
		return manifest, nil, nil
	}
	var captured tmuxpkg.SessionSpec
	service := New(boundTerminalStore{}, fakeTmux{exists: true, specSink: &captured}, nil, workspace, nil, nil)
	if _, err := service.EnsureForManifest(context.Background(), contract.ResolveOptions{}, manifest, true, nil, nil); err == nil {
		t.Fatal("reused stale terminal")
	}
	if captured.Name != "" {
		t.Fatal("ensured the session before validating the claim")
	}
}
