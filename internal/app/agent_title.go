package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GenerateConversationTitle asks the configured default driver for a title.
// The driver owns the auxiliary request; no primary conversation is acquired.
func (a *App) GenerateConversationTitle(ctx context.Context, prompt string) (string, error) {
	definition, configured := a.AgentsConfig[a.DefaultAgent]
	if !configured || strings.TrimSpace(definition.Driver) == "" {
		return "", fmt.Errorf("default agent has no configured conversation driver")
	}
	backend, ok := a.ConversationBackends[definition.Driver].(interface {
		GenerateTitle(context.Context, string, string, json.RawMessage) (string, error)
	})
	if !ok {
		return "", fmt.Errorf("driver %q does not support title generation", definition.Driver)
	}
	raw, err := a.driverConfigJSON(definition)
	if err != nil {
		return "", err
	}
	return backend.GenerateTitle(ctx, prompt, a.DefaultAgent, raw)
}
