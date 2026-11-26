package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/process"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

type settingsBackend struct {
	ConversationBackend
	validated []map[string]any
	reject    bool
}

func (b *settingsBackend) ValidateConfig(_ context.Context, _ string, raw json.RawMessage) error {
	var values map[string]any
	_ = json.Unmarshal(raw, &values)
	b.validated = append(b.validated, values)
	if b.reject {
		return errors.New("unknown model")
	}
	return nil
}

func settingsApp(t *testing.T) (*App, *settingsBackend, model.Manifest) {
	t.Helper()
	a, _, m := newCoreTestApp(t, time.Now(), model.StateActive, false)
	backend := &settingsBackend{}
	a.StaticAgentProfiles.Default = "pi"
	a.StaticAgentProfiles.Profiles = []model.AgentProfile{{Name: "pi", Driver: "pi", Config: map[string]any{"model": "profile/model", "executable": "/bin/pi"}}}
	a.DriversConfig = map[string]config.DriverDefinitionConfig{"pi": {Defaults: map[string]any{"reasoning": "low"}}}
	a.ConversationBackends = map[string]ConversationBackend{"pi": backend}
	return a, backend, m
}

func TestSetAgentSettingsSavesValidatedOverridesForTheNextRuntime(t *testing.T) {
	a, backend, m := settingsApp(t)
	ctx := context.Background()
	result, err := a.SetAgentSettings(ctx, SetAgentSettingsOptions{ResolveOptions: ResolveOptions{Selector: m.ID}, Settings: map[string]string{"model": "other/model", "reasoning": "high"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changed) != 2 || len(result.Pending) != 2 || len(result.Applied) != 0 {
		t.Fatalf("result = %+v", result)
	}
	// The driver validated the merged configuration, item values winning.
	if got := backend.validated[0]; got["model"] != "other/model" || got["reasoning"] != "high" || got["executable"] != "/bin/pi" {
		t.Fatalf("validated %v", got)
	}
	saved, _ := a.Store.LoadManifest(m.ID)
	if saved.AgentSettings["model"] != "other/model" || saved.AgentSettings["reasoning"] != "high" {
		t.Fatalf("saved %v", saved.AgentSettings)
	}
	view, err := a.AgentSettings(ctx, ResolveOptions{Selector: m.ID})
	if err != nil || view.Values["model"] != "other/model" || view.Values["reasoning"] != "high" || view.Overrides["reasoning"] != "high" || view.Values["executable"] != "" {
		t.Fatalf("view=%+v err=%v", view, err)
	}

	// An empty value resets to the profile; unchanged values are not resaved.
	result, err = a.SetAgentSettings(ctx, SetAgentSettingsOptions{ResolveOptions: ResolveOptions{Selector: m.ID}, Settings: map[string]string{"reasoning": "", "model": "other/model"}})
	if err != nil || len(result.Changed) != 1 || result.Changed[0] != "reasoning" {
		t.Fatalf("reset result=%+v err=%v", result, err)
	}
	saved, _ = a.Store.LoadManifest(m.ID)
	if _, ok := saved.AgentSettings["reasoning"]; ok || saved.AgentSettings["model"] != "other/model" {
		t.Fatalf("after reset %v", saved.AgentSettings)
	}
}

func TestSetAgentSettingsRefusesUnsafeOrInvalidChanges(t *testing.T) {
	a, backend, m := settingsApp(t)
	ctx := context.Background()
	for _, settings := range []map[string]string{{"executable": "/tmp/evil"}, {"thinking": "high"}, {"reasoning": "extreme"}} {
		if _, err := a.SetAgentSettings(ctx, SetAgentSettingsOptions{ResolveOptions: ResolveOptions{Selector: m.ID}, Settings: settings}); err == nil {
			t.Fatalf("%v accepted", settings)
		}
	}
	backend.reject = true
	if _, err := a.SetAgentSettings(ctx, SetAgentSettingsOptions{ResolveOptions: ResolveOptions{Selector: m.ID}, Settings: map[string]string{"model": "nope"}}); err == nil {
		t.Fatal("driver rejection ignored")
	}
	saved, _ := a.Store.LoadManifest(m.ID)
	if len(saved.AgentSettings) != 0 {
		t.Fatalf("rejected settings were saved: %v", saved.AgentSettings)
	}
}

type fakeOutcomes struct {
	// commands maps a command to its value; the agent rejects reject.
	reject   string
	commands map[string]string
}

func (f *fakeOutcomes) Cursor(context.Context, string) (uint64, error) { return 7, nil }
func (f *fakeOutcomes) Wait(_ context.Context, _ string, cursor uint64, commandID string, _ time.Duration) (bool, string, error) {
	if cursor != 7 {
		return false, "", errors.New("wrong cursor")
	}
	return f.commands[commandID] != f.reject, "rejected", nil
}

func TestSetAgentSettingsAppliesLiveWhereTheRuntimeCan(t *testing.T) {
	a, _, m := settingsApp(t)
	st := a.Store.(*store.Store)
	a.Process = process.New()
	info, err := a.Process.Info(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	a.AgentRuntimeSocketRoot = testutil.ShortTempDir(t)
	runtime := model.AgentRuntime{ID: "live", WorkItemID: m.ID, HostPID: info.PID, HostProcessGroup: info.PGRP, HostStartTime: info.StartTime, Mode: "headless", Capabilities: model.RuntimeCapabilities{Prompt: true, Control: model.ControlStructured, Settings: []string{"model"}}}
	if err := st.SaveAgentRuntime(context.Background(), m.ID, runtime); err != nil {
		t.Fatal(err)
	}
	server, err := agent.ListenControlSocket(a.runtimeControlSocketPath(m.ID, &runtime))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	outcomes := &fakeOutcomes{reject: "rejected/model", commands: map[string]string{}}
	a.ControlOutcomes = outcomes
	go func() {
		for req := range server.Requests() {
			if req.Command.Type == agent.CommandSet && req.Command.Setting != nil {
				outcomes.commands[req.Command.ID] = req.Command.Setting.Value
				if req.Command.Setting.Value == "refused/on-socket" {
					req.Respond(errors.New("unknown model"))
					continue
				}
			}
			req.Respond(nil)
		}
	}()
	a.Leases = &controllerTestLeases{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := a.SetAgentSettings(ctx, SetAgentSettingsOptions{ResolveOptions: ResolveOptions{Selector: m.ID}, Settings: map[string]string{"model": "other/model", "reasoning": "high"}})
	if err != nil {
		t.Fatal(err)
	}
	// model is applied; reasoning cannot change live in this runtime, so it
	// waits for the next one.
	if len(result.Applied) != 1 || result.Applied[0] != "model" || len(result.Pending) != 1 || result.Pending[0] != "reasoning" || len(result.Warnings) != 0 {
		t.Fatalf("result = %+v", result)
	}
	// The agent rejects the model, so that value is not kept; the rest is.
	result, err = a.SetAgentSettings(ctx, SetAgentSettingsOptions{ResolveOptions: ResolveOptions{Selector: m.ID}, Settings: map[string]string{"model": "rejected/model", "reasoning": "low"}})
	if err != nil || len(result.Pending) != 1 || len(result.Warnings) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	saved, _ := st.LoadManifest(m.ID)
	if len(saved.AgentSettings) != 2 || saved.AgentSettings["model"] != "other/model" || saved.AgentSettings["reasoning"] != "low" {
		t.Fatalf("saved %v", saved.AgentSettings)
	}
	// A change the agent rejects entirely is an error and leaves nothing saved.
	if _, err := a.SetAgentSettings(ctx, SetAgentSettingsOptions{ResolveOptions: ResolveOptions{Selector: m.ID}, Settings: map[string]string{"model": "rejected/model"}}); err == nil {
		t.Fatal("fully rejected change reported success")
	}
	saved, _ = st.LoadManifest(m.ID)
	if saved.AgentSettings["model"] != "other/model" {
		t.Fatalf("rejected model saved: %v", saved.AgentSettings)
	}
	// A driver may refuse on the control socket itself; that is a rejection too.
	if _, err := a.SetAgentSettings(ctx, SetAgentSettingsOptions{ResolveOptions: ResolveOptions{Selector: m.ID}, Settings: map[string]string{"model": "refused/on-socket"}}); err == nil {
		t.Fatal("socket refusal reported success")
	}
	saved, _ = st.LoadManifest(m.ID)
	if saved.AgentSettings["model"] != "other/model" {
		t.Fatalf("socket-refused model replaced the previous one: %v", saved.AgentSettings)
	}
}

// A runtime that has just started is alive before its driver reports what it
// can change live; a setting changed then is applied once it reports.
func TestSetAgentSettingsWaitsForAStartingRuntime(t *testing.T) {
	a, _, m := settingsApp(t)
	st := a.Store.(*store.Store)
	a.Process = process.New()
	info, err := a.Process.Info(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	a.AgentRuntimeSocketRoot = testutil.ShortTempDir(t)
	starting := model.AgentRuntime{ID: "starting", WorkItemID: m.ID, HostPID: info.PID, HostProcessGroup: info.PGRP, HostStartTime: info.StartTime, Mode: "headless"}
	if err := st.SaveAgentRuntime(context.Background(), m.ID, starting); err != nil {
		t.Fatal(err)
	}
	server, err := agent.ListenControlSocket(a.runtimeControlSocketPath(m.ID, &starting))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		for req := range server.Requests() {
			req.Respond(nil)
		}
	}()
	go func() {
		time.Sleep(300 * time.Millisecond)
		ready := starting
		ready.Capabilities = model.RuntimeCapabilities{Prompt: true, Control: model.ControlStructured, Settings: []string{"model"}}
		_ = st.SaveAgentRuntime(context.Background(), m.ID, ready)
	}()
	a.ControlOutcomes = &fakeOutcomes{reject: "none", commands: map[string]string{}}
	a.Leases = &controllerTestLeases{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := a.SetAgentSettings(ctx, SetAgentSettingsOptions{ResolveOptions: ResolveOptions{Selector: m.ID}, Settings: map[string]string{"model": "other/model"}})
	if err != nil || len(result.Applied) != 1 || len(result.Pending) != 0 {
		t.Fatalf("result = %+v err=%v", result, err)
	}
}

// A reported value becomes the item's own only where neither the item nor
// its profile chose one.
func TestAdoptReportedSettingsKeepsChosenValues(t *testing.T) {
	a, _, m := settingsApp(t)
	ctx := context.Background()
	definition := model.AgentProfile{Driver: "pi", Config: map[string]any{"executable": "/bin/pi"}}
	a.DriversConfig = map[string]config.DriverDefinitionConfig{"pi": {}}
	m.AgentSettings = map[string]string{"reasoning": "high"}
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := a.adoptReportedSettings(ctx, m.ID, definition, map[string]string{"model": "openai/gpt", "reasoning": "medium"}, false); err != nil {
		t.Fatal(err)
	}
	saved, _ := a.Store.LoadManifest(m.ID)
	if want := map[string]string{"model": "openai/gpt", "reasoning": "high"}; !reflect.DeepEqual(saved.AgentSettings, want) {
		t.Fatalf("settings = %v, want %v", saved.AgentSettings, want)
	}
	// Once chosen, a later report leaves the item's model alone.
	if err := a.adoptReportedSettings(ctx, m.ID, definition, map[string]string{"model": "openai/other"}, false); err != nil {
		t.Fatal(err)
	}
	saved, _ = a.Store.LoadManifest(m.ID)
	if saved.AgentSettings["model"] != "openai/gpt" {
		t.Fatalf("model = %q", saved.AgentSettings["model"])
	}
	// A profile's model stays the profile's to change.
	saved.AgentSettings = nil
	if err := a.Store.SaveManifest(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if err := a.adoptReportedSettings(ctx, m.ID, a.Agents()["pi"], map[string]string{"model": "profile/model"}, false); err != nil {
		t.Fatal(err)
	}
	saved, _ = a.Store.LoadManifest(m.ID)
	if len(saved.AgentSettings) != 0 {
		t.Fatalf("profile model adopted: %v", saved.AgentSettings)
	}
	// A later report is a change, such as a model picked in the agent's
	// own interface, and the item keeps it.
	if err := a.adoptReportedSettings(ctx, m.ID, a.Agents()["pi"], map[string]string{"model": "other/model", "reasoning": "low"}, true); err != nil {
		t.Fatal(err)
	}
	saved, _ = a.Store.LoadManifest(m.ID)
	if want := map[string]string{"model": "other/model", "reasoning": "low"}; !reflect.DeepEqual(saved.AgentSettings, want) {
		t.Fatalf("after a change: %v, want %v", saved.AgentSettings, want)
	}
}

// flakyCatalog fails its first catalog and runtime modes requests.
type flakyCatalog struct {
	ConversationBackend
	catalogCalls, modesCalls int
}

func (b *flakyCatalog) ModelCatalog(context.Context, string, json.RawMessage) (driverprotocol.ModelCatalog, error) {
	b.catalogCalls++
	if b.catalogCalls == 1 {
		return driverprotocol.ModelCatalog{}, errors.New("backend busy")
	}
	return driverprotocol.ModelCatalog{Models: []driverprotocol.ModelOption{{Value: "m"}}}, nil
}

func (b *flakyCatalog) RuntimeModes(context.Context, string, json.RawMessage) (driverprotocol.RuntimeModes, error) {
	b.modesCalls++
	if b.modesCalls == 1 {
		return driverprotocol.RuntimeModes{}, errors.New("backend busy")
	}
	return driverprotocol.RuntimeModes{Modes: map[driverprotocol.Mode]driverprotocol.ModeCapabilities{driverprotocol.ModeHeadless: {Control: driverprotocol.ControlStructured}}}, nil
}

func TestDriverAnswersAreCachedButFailuresAreNot(t *testing.T) {
	a, _, _ := settingsApp(t)
	backend := &flakyCatalog{}
	a.ConversationBackends = map[string]ConversationBackend{"pi": backend}
	ctx := context.Background()
	if _, err := a.agentModelCatalog(ctx, "pi"); err == nil {
		t.Fatal("first catalog request should fail")
	}
	for range 2 {
		if catalog, err := a.agentModelCatalog(ctx, "pi"); err != nil || len(catalog.Models) != 1 {
			t.Fatalf("catalog after a failure: %v %v", catalog, err)
		}
	}
	if backend.catalogCalls != 2 {
		t.Fatalf("catalog asked %d times, want 2", backend.catalogCalls)
	}
	if modes := a.AgentRuntimeModes(ctx, "pi"); len(modes.Modes) == 1 {
		t.Fatalf("failed modes request answered %v", modes)
	}
	for range 2 {
		if modes := a.AgentRuntimeModes(ctx, "pi"); len(modes.Modes) != 1 {
			t.Fatalf("modes after a failure: %v", modes)
		}
	}
	// An edited profile is asked again.
	a.StaticAgentProfiles.Profiles[0].Config["model"] = "edited/model"
	a.AgentRuntimeModes(ctx, "pi")
	if backend.modesCalls != 3 {
		t.Fatalf("modes asked %d times, want 3", backend.modesCalls)
	}
	// A driver that predates the question gets the legacy modes.
	older, _, _ := settingsApp(t)
	older.ConversationBackends = map[string]ConversationBackend{"pi": &modesBackend{err: errors.New("unsupported operation")}}
	if got := older.AgentRuntimeModes(ctx, "pi"); got.Modes[driverprotocol.ModeHeadless].Control != driverprotocol.ControlStructured || !got.Modes[driverprotocol.ModeTUI].Terminal {
		t.Fatalf("older driver did not fall back to legacy modes: %+v", got)
	}
}
