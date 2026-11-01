package app

import (
	"context"

	"github.com/regb/workitem/internal/model"
)

// agentProfiles returns the daemon's agent profiles, read for each use so a
// long-running process sees changes at once, or StaticAgentProfiles.
func (a *App) agentProfiles() model.AgentProfiles {
	if a.LoadAgentProfiles == nil {
		return a.StaticAgentProfiles
	}
	profiles, err := a.LoadAgentProfiles(context.Background())
	if err != nil {
		return model.AgentProfiles{}
	}
	return profiles
}

// Agents returns the agent profiles by name.
func (a *App) Agents() map[string]model.AgentProfile {
	agents := map[string]model.AgentProfile{}
	for _, profile := range a.agentProfiles().Profiles {
		agents[profile.Name] = profile
	}
	return agents
}

// DefaultProfile names the default agent profile.
func (a *App) DefaultProfile() string {
	return a.agentProfiles().Default
}
