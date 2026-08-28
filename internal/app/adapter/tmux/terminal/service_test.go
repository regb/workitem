package terminal

import (
	"context"
	"errors"
	"testing"

	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
	tmuxpkg "github.com/regb/workitem/internal/tmux"
)

type fakeTmux struct {
	exists     bool
	current    string
	err        error
	specSink   *tmuxpkg.SessionSpec
	attachSink *int
	clientSink *string
}

func (f fakeTmux) HasSession(context.Context, string) (bool, error) { return f.exists, f.err }
func (f fakeTmux) CurrentSession(context.Context) (string, error)   { return f.current, f.err }
func (f fakeTmux) EnsureSession(_ context.Context, spec tmuxpkg.SessionSpec) (bool, error) {
	if f.specSink != nil {
		*f.specSink = spec
	}
	return false, f.err
}
func (f fakeTmux) AttachOrSwitch(context.Context, string, bool) error {
	if f.attachSink != nil {
		*f.attachSink++
	}
	return f.err
}
func (f fakeTmux) AttachOrSwitchClient(_ context.Context, _ string, _ bool, client string) error {
	if f.attachSink != nil {
		*f.attachSink++
	}
	if f.clientSink != nil {
		*f.clientSink = client
	}
	return f.err
}
func (f fakeTmux) KillSession(context.Context, string) error { return f.err }

type fakeStore struct{}

func (fakeStore) ItemDir(string) string                                  { return "/items/item-1" }
func (fakeStore) LoadAgentRuntime(string) (*model.AgentRuntime, error)   { return nil, nil }
func (fakeStore) RemoveTerminalRuntime(context.Context, string) error    { return nil }
func (fakeStore) AppendEvent(context.Context, string, model.Event) error { return nil }

func TestEnsureForManifestAppliesEnvironmentBeforeSessionCreation(t *testing.T) {
	path := "/checkout"
	manifest := model.Manifest{ID: "item-1", Repository: model.Repository{RootAtCreation: "/repo"}, Checkout: model.Checkout{Path: &path}}
	workspace := func(context.Context, contract.ResolveOptions, model.Manifest, bool) (model.Manifest, []string, error) {
		return manifest, nil, nil
	}
	var captured tmuxpkg.SessionSpec
	service := New(fakeStore{}, fakeTmux{specSink: &captured}, nil, workspace, nil, nil)
	_, err := service.EnsureForManifestWithEnvironment(context.Background(), contract.ResolveOptions{}, manifest, true, map[string]string{"PROJECT_TOKEN": "loaded", "PATH": "/project/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if captured.Env["PROJECT_TOKEN"] != "loaded" || captured.Env["PATH"] != "/project/bin" || captured.Env["WI_ID"] != "item-1" {
		t.Fatalf("session environment=%+v", captured.Env)
	}
}

func TestEnterExistingBypassesWorkspaceEnsureForRepairAccess(t *testing.T) {
	manifest := model.Manifest{ID: "item-1", State: model.StateWorking, Checkout: model.Checkout{}}
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return manifest, nil }
	workspace := func(context.Context, contract.ResolveOptions, model.Manifest, bool) (model.Manifest, []string, error) {
		return model.Manifest{}, nil, errors.New("workspace ensure must not run")
	}
	attached := 0
	res, err := New(fakeStore{}, fakeTmux{exists: true, attachSink: &attached}, resolve, workspace, nil, nil).EnterExisting(context.Background(), contract.ResolveOptions{Env: map[string]string{"TMUX": "yes", "WI_TMUX_CLIENT": "client-7"}}, true)
	if err != nil || !res.Attached || attached != 1 {
		t.Fatalf("result=%+v attached=%d err=%v", res, attached, err)
	}
}

func TestEnterAlwaysSwitchesCapturedOriginatingClientAfterSessionCreation(t *testing.T) {
	manifest := model.Manifest{ID: "item-1", Repository: model.Repository{RootAtCreation: "/repo"}, Checkout: model.Checkout{}}
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) { return manifest, nil }
	workspace := func(context.Context, contract.ResolveOptions, model.Manifest, bool) (model.Manifest, []string, error) {
		return manifest, nil, nil
	}
	attached := 0
	client := ""
	mux := fakeTmux{current: manifest.TerminalSessionName(), attachSink: &attached, clientSink: &client}
	res, err := New(fakeStore{}, mux, resolve, workspace, nil, nil).Enter(context.Background(), contract.ResolveOptions{Env: map[string]string{"TMUX": "yes", "WI_TMUX_CLIENT": "client-7"}}, true)
	if err != nil || !res.Attached || attached != 1 || client != "client-7" {
		t.Fatalf("result=%+v attached=%d client=%q err=%v", res, attached, client, err)
	}
}

func TestStatus(t *testing.T) {
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{ID: "item-1"}, nil
	}
	res, err := New(nil, fakeTmux{exists: true, current: "wi-item-1"}, resolve, nil, nil, nil).Status(context.Background(), contract.ResolveOptions{Env: map[string]string{"TMUX": "yes"}})
	if err != nil || !res.Exists || !res.Inspected || !res.Current || len(res.Warnings) != 0 {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}
func TestStatusReportsInspectionWarning(t *testing.T) {
	resolve := func(context.Context, contract.ResolveOptions) (model.Manifest, error) {
		return model.Manifest{ID: "item-1"}, nil
	}
	res, err := New(nil, fakeTmux{err: errors.New("boom")}, resolve, nil, nil, nil).Status(context.Background(), contract.ResolveOptions{})
	if err != nil || len(res.Warnings) != 1 {
		t.Fatalf("result=%+v err=%v", res, err)
	}
}
