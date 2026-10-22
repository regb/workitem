package app

import (
	"context"
	"encoding/json"
	"sync"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/model"
)

// RuntimeModesReporter is implemented by conversation backends whose driver
// reports its runtime modes (runtime.modes).
type RuntimeModesReporter interface {
	RuntimeModes(ctx context.Context, agent string, config json.RawMessage) (driverprotocol.RuntimeModes, error)
}

// legacyRuntimeModes is what wi assumed before drivers reported modes: a
// headless runtime has structured control and a TUI runtime has a terminal.
func legacyRuntimeModes() driverprotocol.RuntimeModes {
	return driverprotocol.RuntimeModes{Modes: map[driverprotocol.Mode]driverprotocol.ModeCapabilities{
		driverprotocol.ModeTUI:      {Terminal: true},
		driverprotocol.ModeHeadless: {Control: driverprotocol.ControlStructured},
	}}
}

// runtimeModesCache keeps each profile's answer for the life of the process.
// Profile configuration only changes on restart, and control is planned on
// every conversation refresh, so asking the driver each time would be costly.
type runtimeModesCache struct {
	mu    sync.Mutex
	modes map[string]driverprotocol.RuntimeModes
}

// AgentRuntimeModes reports the runtime modes of an agent profile, falling
// back to the legacy assumption when the driver does not report them.
func (a *App) AgentRuntimeModes(ctx context.Context, name string) driverprotocol.RuntimeModes {
	name, err := a.resolveAgentName(name)
	if err != nil {
		return legacyRuntimeModes()
	}
	cache := &a.runtimeModes
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.modes == nil {
		cache.modes = map[string]driverprotocol.RuntimeModes{}
	}
	if modes, ok := cache.modes[name]; ok {
		return modes
	}
	modes := legacyRuntimeModes()
	definition := a.AgentsConfig[name]
	if reporter, ok := a.ConversationBackends[definition.Driver].(RuntimeModesReporter); ok {
		if raw, err := a.driverConfigJSON(definition); err == nil {
			if reported, err := reporter.RuntimeModes(ctx, name, raw); err == nil {
				modes = reported
			}
		}
	}
	cache.modes[name] = modes
	return modes
}

// controlMode chooses the runtime mode that gives a holder its channel: a
// structured control channel for the web, a terminal for tmux. It prefers
// the mode that holder conventionally uses and reports false when no mode
// of the profile offers the channel.
func controlMode(modes driverprotocol.RuntimeModes, holder string) (agent.Mode, bool) {
	want := func(c driverprotocol.ModeCapabilities) bool { return c.Control == driverprotocol.ControlStructured }
	preferred := driverprotocol.ModeHeadless
	if holder == model.LeaseHolderTerminal {
		want = func(c driverprotocol.ModeCapabilities) bool { return c.Terminal }
		preferred = driverprotocol.ModeTUI
	}
	candidates := modes.ModesWith(want, preferred)
	if len(candidates) == 0 {
		return "", false
	}
	return agent.Mode(candidates[0]), true
}
