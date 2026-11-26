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
	if _, ok := a.Agents()[name]; !ok {
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
	for name, definition := range a.Agents() {
		if driver == "" || definition.Driver == driver {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
