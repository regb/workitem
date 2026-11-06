package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GenerateConversationTitle asks the configured default driver for a title.
func (a *App) GenerateConversationTitle(ctx context.Context, prompt string) (string, error) {
	return a.generateTitle(ctx, a.DefaultProfile(), prompt)
}

// GenerateItemTitle uses the item's profile without acquiring a conversation.
func (a *App) GenerateItemTitle(ctx context.Context, opts ResolveOptions, prompt string) (string, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return "", err
	}
	name, err := a.itemAgentName(m)
	if err != nil {
		return "", err
	}
	return a.generateTitle(ctx, name, prompt)
}
func (a *App) generateTitle(ctx context.Context, name, prompt string) (string, error) {
	definition, configured := a.Agents()[name]
	if !configured || strings.TrimSpace(definition.Driver) == "" {
		return "", fmt.Errorf("agent profile %q has no configured conversation driver", name)
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
	return backend.GenerateTitle(ctx, prompt, name, raw)
}
