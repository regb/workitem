package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/regb/workitem/internal/model"
)

// MissingAgentProfileError reports an item whose agent profile is no longer
// configured. Choices are the profiles that can take it over.
type MissingAgentProfileError struct {
	WorkItemID string
	Profile    string
	Driver     string
	Choices    []string
}

func (e *MissingAgentProfileError) Error() string {
	message := fmt.Sprintf("work item %s uses agent profile %q, which is no longer configured; move it to another with `wi agent profile set --item %s PROFILE`", e.WorkItemID, e.Profile, e.WorkItemID)
	switch {
	case len(e.Choices) > 0:
		message += fmt.Sprintf(" (profiles that can resume it: %s)", strings.Join(e.Choices, ", "))
	case e.Driver != "":
		message += fmt.Sprintf(" (no configured profile uses its conversation's driver %q)", e.Driver)
	}
	return message
}

// itemAgentName resolves an item's agent profile, explaining how to recover
// when the profile was removed from the configuration.
func (a *App) itemAgentName(m model.Manifest) (string, error) {
	name := a.agentProfileName(m.Agent)
	if _, ok := a.AgentsConfig[name]; !ok {
		driver := conversationDriver(m)
		return "", &MissingAgentProfileError{WorkItemID: m.ID, Profile: name, Driver: driver, Choices: a.profilesFor(driver)}
	}
	return a.resolveAgentName(name)
}

// validateItemAgent checks an item's profile and settings before a new
// runtime starts.
func (a *App) validateItemAgent(ctx context.Context, m model.Manifest) error {
	if _, err := a.itemAgentName(m); err != nil {
		return err
	}
	return a.ValidateAgentConfig(ctx, m.Agent, m.AgentSettings)
}

// conversationDriver is the driver that owns an item's conversation, or ""
// before it has one.
func conversationDriver(m model.Manifest) string {
	if m.PrimaryConversation == nil {
		return ""
	}
	return m.PrimaryConversation.Driver
}

// profilesFor lists the configured profiles of a driver, or all of them.
func (a *App) profilesFor(driver string) []string {
	names := []string{}
	for name, definition := range a.AgentsConfig {
		if driver == "" || definition.Driver == driver {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// SetAgentProfile moves an item to another agent profile while no agent runs
// for it. Its conversation belongs to one driver, so only a profile of that
// driver can resume it. Settings changes the item's own model and reasoning
// in the same step, as SetAgentSettings does, since they must suit the new
// profile.
func (a *App) SetAgentProfile(ctx context.Context, opts ResolveOptions, profile string, settings map[string]string) (model.Manifest, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return model.Manifest{}, err
	}
	defer operation.Release()
	if m.State == model.StateClosed {
		return model.Manifest{}, fmt.Errorf("work item %s is closed", m.ID)
	}
	name, err := a.resolveAgentName(profile)
	if err != nil {
		return model.Manifest{}, err
	}
	if driver := conversationDriver(m); driver != "" && a.AgentsConfig[name].Driver != driver {
		return model.Manifest{}, fmt.Errorf("the conversation of work item %s belongs to driver %q, which profile %q does not use; profiles that can resume it: %s", m.ID, driver, name, strings.Join(a.profilesFor(driver), ", "))
	}
	overrides, changed, err := mergeSettings(m.AgentSettings, settings)
	if err != nil {
		return model.Manifest{}, err
	}
	if name == a.agentProfileName(m.Agent) && len(changed) == 0 {
		return m, nil
	}
	runtime, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil {
		return model.Manifest{}, err
	}
	if a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		return model.Manifest{}, fmt.Errorf("an agent is running for work item %s; stop it first with `wi agent runtime stop`", m.ID)
	}
	if err := a.ValidateAgentConfig(ctx, name, overrides); err != nil {
		return model.Manifest{}, fmt.Errorf("%w; change the item's model or reasoning in the same command, such as model=VALUE, or reset it with model=", err)
	}
	if len(overrides) == 0 {
		overrides = nil
	}
	previous := m.Agent
	m.Agent = name
	m.AgentSettings = overrides
	m.UpdatedAt = a.now()
	if err := a.Store.SaveManifest(ctx, m); err != nil {
		return model.Manifest{}, err
	}
	if err := a.Store.AppendEvent(ctx, m.ID, model.NewEvent(m.UpdatedAt, "agent.profile_changed", "user", map[string]any{"from": previous, "to": name, "settings": changed})); err != nil {
		return m, err
	}
	return m, nil
}
