package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/regb/workitem/internal/config"
)

// ValidateAgentConfig asks the driver to validate merged profile settings without
// materializing a checkout, conversation, terminal, or runtime.
func (a *App) ValidateAgentConfig(ctx context.Context, name string) error {
	name, err := a.resolveAgentName(name)
	if err != nil {
		return err
	}
	definition := a.AgentsConfig[name]
	backend := a.ConversationBackends[definition.Driver]
	if backend == nil {
		return fmt.Errorf("configuration validator for driver %q is not configured", definition.Driver)
	}
	raw, err := a.driverConfigJSON(definition)
	if err != nil {
		return err
	}
	if err := backend.ValidateConfig(ctx, name, raw); err != nil {
		return fmt.Errorf("agent %q driver %q configuration: %w", name, definition.Driver, err)
	}
	return nil
}

// ValidateItemAgentConfig checks future launch settings only when a new owner is
// needed. Editing configuration must not block access to an existing owner.
func (a *App) ValidateItemAgentConfig(ctx context.Context, opts ResolveOptions) error {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return err
	}
	runtime, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil {
		return err
	}
	if a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		return nil
	}
	return a.ValidateAgentConfig(ctx, m.Agent)
}

// driverConfigJSON applies agent overrides to driver defaults without changing
// either configured map. Conversation and auxiliary operations use the same merge.
func (a *App) driverConfigJSON(definition config.AgentDefinitionConfig) (json.RawMessage, error) {
	values := make(map[string]any)
	for key, value := range a.DriversConfig[definition.Driver].Defaults {
		values[key] = value
	}
	for key, value := range definition.Config {
		values[key] = value
	}
	return json.Marshal(values)
}
