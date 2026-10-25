package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

// ControlOutcomes waits for a driver's verdict on a submitted command. Some
// drivers acknowledge the control socket on handoff and accept or reject the
// command later through a runtime event.
type ControlOutcomes interface {
	// Cursor marks the item's latest runtime event before a submission.
	Cursor(ctx context.Context, itemID string) (uint64, error)
	// Wait reports whether the driver accepted commandID after cursor. A
	// timeout returns an error: the outcome is unknown.
	Wait(ctx context.Context, itemID string, cursor uint64, commandID string, timeout time.Duration) (accepted bool, reason string, err error)
}

// AgentSettingsView is an item's model and reasoning settings and the choices
// its driver offers.
type AgentSettingsView struct {
	WorkItemID string `json:"work_item_id"`
	Agent      string `json:"agent"`
	Driver     string `json:"driver"`
	// Values are the effective settings: the profile's, then the item's.
	Values map[string]string `json:"values"`
	// Overrides are the item's own settings.
	Overrides     map[string]string            `json:"overrides"`
	Catalog       *driverprotocol.ModelCatalog `json:"catalog,omitempty"`
	CatalogError  string                       `json:"catalog_error,omitempty"`
	RuntimeOnline bool                         `json:"runtime_online"`
	RuntimeMode   string                       `json:"runtime_mode,omitempty"`
	// Live lists the settings the running runtime can change in place.
	Live []string `json:"live"`
}

// SetAgentSettingsOptions changes an item's settings. An empty value resets
// a setting to the profile's. Controller fields identify a browser lease, as
// for AgentControl.
type SetAgentSettingsOptions struct {
	ResolveOptions
	Settings map[string]string
	// Restart allows restarting a runtime that cannot apply a change live,
	// on the same conversation and in the same mode.
	Restart            bool
	ControllerHolder   string
	ControllerClientID string
	ControllerLeaseID  string
}

type SetAgentSettingsResult struct {
	WorkItemID string            `json:"work_item_id"`
	Overrides  map[string]string `json:"overrides"`
	Changed    []string          `json:"changed"`
	// Applied were changed in the running agent; Pending apply when the
	// runtime next starts. Restarted reports a restart that applied them.
	Applied   []string `json:"applied"`
	Pending   []string `json:"pending"`
	Restarted bool     `json:"restarted,omitempty"`
	Warnings  []string `json:"warnings"`
}

// catalogCache keeps model catalogs briefly: listing them may start the
// backend's own catalog command.
type catalogCache struct {
	mu      sync.Mutex
	entries map[string]catalogEntry
}

type catalogEntry struct {
	catalog driverprotocol.ModelCatalog
	err     error
	at      time.Time
}

// ModelCatalogReporter is implemented by backends whose driver answers
// config.models.
type ModelCatalogReporter interface {
	ModelCatalog(ctx context.Context, agent string, config json.RawMessage) (driverprotocol.ModelCatalog, error)
}

func (a *App) agentModelCatalog(ctx context.Context, name string) (driverprotocol.ModelCatalog, error) {
	a.modelCatalogs.mu.Lock()
	defer a.modelCatalogs.mu.Unlock()
	if a.modelCatalogs.entries == nil {
		a.modelCatalogs.entries = map[string]catalogEntry{}
	}
	if entry, ok := a.modelCatalogs.entries[name]; ok && time.Since(entry.at) < 10*time.Minute {
		return entry.catalog, entry.err
	}
	definition := a.AgentsConfig[name]
	reporter, ok := a.ConversationBackends[definition.Driver].(ModelCatalogReporter)
	if !ok {
		return driverprotocol.ModelCatalog{}, fmt.Errorf("driver %q does not list models", definition.Driver)
	}
	raw, err := a.driverConfigJSON(definition)
	if err != nil {
		return driverprotocol.ModelCatalog{}, err
	}
	catalog, err := reporter.ModelCatalog(ctx, name, raw)
	a.modelCatalogs.entries[name] = catalogEntry{catalog: catalog, err: err, at: time.Now()}
	return catalog, err
}

// AgentSettings reports an item's effective settings and the driver's choices.
func (a *App) AgentSettings(ctx context.Context, opts ResolveOptions) (AgentSettingsView, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return AgentSettingsView{}, err
	}
	name, err := a.resolveAgentName(m.Agent)
	if err != nil {
		return AgentSettingsView{}, err
	}
	definition := a.AgentsConfig[name]
	view := AgentSettingsView{WorkItemID: m.ID, Agent: name, Driver: definition.Driver, Values: map[string]string{}, Overrides: copySettings(m.AgentSettings), Live: []string{}}
	for key, value := range a.driverConfig(definition, m.AgentSettings) {
		if text, ok := value.(string); ok && key != "executable" {
			view.Values[key] = text
		}
	}
	if catalog, err := a.agentModelCatalog(ctx, name); err == nil {
		view.Catalog = &catalog
	} else {
		view.CatalogError = err.Error()
	}
	runtime, err := a.Store.LoadAgentRuntime(m.ID)
	if err == nil && a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		view.RuntimeOnline, view.RuntimeMode = true, runtime.Mode
		view.Live = append(view.Live, runtime.Capabilities.Settings...)
	}
	return view, nil
}

// SetAgentSettings saves an item's settings and applies them to a running
// agent: live where the runtime supports it, otherwise by restarting it on
// the same conversation when Restart is set, or on its next start.
func (a *App) SetAgentSettings(ctx context.Context, opts SetAgentSettingsOptions) (SetAgentSettingsResult, error) {
	if len(opts.Settings) == 0 {
		return SetAgentSettingsResult{}, errors.New("no settings to change")
	}
	m, err := a.ResolveItem(ctx, opts.ResolveOptions)
	if err != nil {
		return SetAgentSettingsResult{}, err
	}
	if m.State == model.StateClosed {
		return SetAgentSettingsResult{}, fmt.Errorf("work item %s is closed", m.ID)
	}
	name, err := a.resolveAgentName(m.Agent)
	if err != nil {
		return SetAgentSettingsResult{}, err
	}
	overrides, changed, err := mergeSettings(m.AgentSettings, opts.Settings)
	if err != nil {
		return SetAgentSettingsResult{}, err
	}
	result := SetAgentSettingsResult{WorkItemID: m.ID, Overrides: overrides, Changed: changed, Applied: []string{}, Pending: []string{}, Warnings: []string{}}
	if len(changed) == 0 {
		return result, nil
	}
	definition := a.AgentsConfig[name]
	backend := a.ConversationBackends[definition.Driver]
	if backend == nil {
		return result, fmt.Errorf("driver %q is not configured", definition.Driver)
	}
	raw, err := json.Marshal(a.driverConfig(definition, overrides))
	if err != nil {
		return result, err
	}
	if err := backend.ValidateConfig(ctx, name, raw); err != nil {
		return result, fmt.Errorf("agent %q settings: %w", name, err)
	}

	// Save first: the settings are durable intent for every later runtime,
	// even if applying them to the running one fails.
	if err := a.saveAgentSettings(ctx, m.ID, overrides); err != nil {
		return result, err
	}
	runtime, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil || !a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		result.Pending = changed
		return result, nil
	}
	if runtime = a.awaitCapabilities(ctx, m.ID, runtime); runtime == nil {
		result.Pending = changed
		return result, nil
	}
	values := a.driverConfig(definition, overrides)
	rejected := []string{}
	for _, setting := range changed {
		value, _ := values[setting].(string)
		if value == "" || !runtime.Capabilities.LiveSetting(setting) {
			// A reset to an unset profile value, or a setting this runtime
			// cannot change, needs a fresh runtime.
			result.Pending = append(result.Pending, setting)
			continue
		}
		if err := a.applyLiveSetting(ctx, m.ID, runtime.ID, setting, value, opts); err != nil {
			if errors.Is(err, errSettingRejected) {
				// The agent refused the value: keeping it would only fail the
				// next start, so the previous value stays.
				rejected = append(rejected, setting)
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s %q was rejected by the agent and not saved", setting, value))
				continue
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("could not change %s in the running agent: %v", setting, err))
			result.Pending = append(result.Pending, setting)
			continue
		}
		result.Applied = append(result.Applied, setting)
	}
	if len(rejected) > 0 {
		reverted := copySettings(overrides)
		for _, setting := range rejected {
			if previous, ok := m.AgentSettings[setting]; ok {
				reverted[setting] = previous
			} else {
				delete(reverted, setting)
			}
		}
		if len(reverted) == 0 {
			reverted = nil
		}
		if err := a.saveAgentSettings(ctx, m.ID, reverted); err != nil {
			return result, err
		}
		result.Overrides = reverted
		result.Changed = without(result.Changed, rejected)
		if len(result.Changed) == 0 {
			return result, fmt.Errorf("the agent rejected %s", strings.Join(rejected, ", "))
		}
	}
	if len(result.Pending) > 0 && opts.Restart {
		if err := a.restartForSettings(ctx, m, runtime, opts); err != nil {
			return result, fmt.Errorf("settings saved, but the restart failed: %w", err)
		}
		result.Restarted = true
		result.Applied = append(result.Applied, result.Pending...)
		result.Pending = []string{}
	}
	return result, nil
}

func (a *App) applyLiveSetting(ctx context.Context, itemID, runtimeID, name, value string, opts SetAgentSettingsOptions) error {
	var cursor uint64
	if a.ControlOutcomes != nil {
		var err error
		if cursor, err = a.ControlOutcomes.Cursor(ctx, itemID); err != nil {
			return err
		}
	}
	submitted, err := a.AgentControl(ctx, AgentControlOptions{ResolveOptions: ResolveOptions{Selector: itemID, CWD: opts.CWD, Env: opts.Env}, CommandType: agent.CommandSet, Setting: &agent.Setting{Name: name, Value: value}, Actor: "operator", ExpectedRuntimeID: runtimeID, ControllerHolder: opts.ControllerHolder, ControllerClientID: opts.ControllerClientID, ControllerLeaseID: opts.ControllerLeaseID})
	// Some drivers refuse on the control socket itself, such as Pi's TUI.
	var refused *driverprotocol.ControlRejectedError
	if errors.As(err, &refused) {
		return fmt.Errorf("%w: %s", errSettingRejected, refused.Message)
	}
	if err != nil || a.ControlOutcomes == nil {
		return err
	}
	accepted, reason, err := a.ControlOutcomes.Wait(ctx, itemID, cursor, submitted.Command.ID, 10*time.Second)
	if err != nil {
		return fmt.Errorf("the agent did not confirm the change: %w", err)
	}
	if !accepted {
		if reason == "" {
			reason = "rejected by the agent"
		}
		return fmt.Errorf("%w: %s", errSettingRejected, reason)
	}
	return nil
}

// errSettingRejected marks a value the running agent refused.
var errSettingRejected = errors.New("rejected by the agent")

func without(values, remove []string) []string {
	result := []string{}
	for _, value := range values {
		drop := false
		for _, other := range remove {
			drop = drop || value == other
		}
		if !drop {
			result = append(result, value)
		}
	}
	return result
}

// restartForSettings stops the runtime and resumes the same conversation in
// the same mode, which picks up the saved settings. A busy agent refuses.
func (a *App) restartForSettings(ctx context.Context, m model.Manifest, runtime *model.AgentRuntime, opts SetAgentSettingsOptions) error {
	if a.AgentBusy(ctx, m.ID) {
		return errors.New("the agent is working; wait for it to finish before restarting")
	}
	unlock, err := a.lockController(ctx, m.ID)
	if err != nil {
		return err
	}
	defer unlock()
	mode := agent.Mode(runtime.Mode)
	if err := a.stopRuntimeForReplacement(ctx, m, runtime.ID, false); err != nil {
		return err
	}
	resolve := ResolveOptions{Selector: m.ID, CWD: opts.CWD, Env: opts.Env}
	ensured, err := a.EnsureAgentRuntime(ctx, resolve, mode)
	if err != nil {
		return err
	}
	started := ensured.Runtime
	return a.waitForAgentRuntimeReady(ctx, m.ID, &started)
}

func (a *App) saveAgentSettings(ctx context.Context, id string, overrides map[string]string) error {
	m, operation, err := a.lockItemOperation(ctx, ResolveOptions{Selector: id})
	if err != nil {
		return err
	}
	defer operation.Release()
	m.AgentSettings = overrides
	m.UpdatedAt = a.now()
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		return err
	}
	return a.Store.AppendEvent(ctx, m.ID, model.NewEvent(m.UpdatedAt, "agent.settings_changed", "user", map[string]any{"settings": sortedKeys(overrides)}))
}

// mergeSettings applies changes to an item's overrides and reports which
// settings changed. An item chooses only its model and reasoning level; the
// rest of the configuration stays with the operator's profile.
func mergeSettings(current, changes map[string]string) (map[string]string, []string, error) {
	next := copySettings(current)
	changed := []string{}
	for name, value := range changes {
		value = strings.TrimSpace(value)
		if name != driverprotocol.SettingModel && name != driverprotocol.SettingReasoning {
			return nil, nil, fmt.Errorf("unknown agent setting %q; an item sets only model and reasoning", name)
		}
		if value == "" {
			if _, ok := next[name]; ok {
				delete(next, name)
				changed = append(changed, name)
			}
			continue
		}
		if err := (agent.Setting{Name: name, Value: value}).Validate(); err != nil {
			return nil, nil, err
		}
		if name == driverprotocol.SettingReasoning && !slices.Contains(driverprotocol.ReasoningLevels, value) {
			return nil, nil, fmt.Errorf("invalid reasoning level %q; levels are %s", value, strings.Join(driverprotocol.ReasoningLevels, ", "))
		}
		if next[name] != value {
			next[name] = value
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	if len(next) == 0 {
		next = nil
	}
	return next, changed, nil
}

func copySettings(values map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range values {
		result[key] = value
	}
	return result
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// capabilitiesWait bounds how long a setting change waits for a runtime that
// is still starting to report what it can change live.
const capabilitiesWait = 10 * time.Second

// awaitCapabilities returns the runtime once its driver has reported its
// capabilities. A runtime started moments ago is alive before it reports
// them, and judging it then would leave its settings for the next start. It
// returns nil if the runtime stops or changes meanwhile.
func (a *App) awaitCapabilities(ctx context.Context, itemID string, runtime *model.AgentRuntime) *model.AgentRuntime {
	reported := func(r *model.AgentRuntime) bool {
		return r.Capabilities.Prompt || r.Capabilities.NativeTUI || r.Capabilities.Headless
	}
	deadline := time.Now().Add(capabilitiesWait)
	for !reported(runtime) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return runtime
		case <-time.After(100 * time.Millisecond):
		}
		current, err := a.Store.LoadAgentRuntime(itemID)
		if err != nil || current == nil || current.ID != runtime.ID || !a.primaryAgentService().ObserveOwnership(current).ProcessAlive {
			return nil
		}
		runtime = current
	}
	return runtime
}
