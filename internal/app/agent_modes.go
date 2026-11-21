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

// runtimeModesCache keeps each profile's answer, keyed by the profile and the
// config it was asked with, so an edited profile is asked again. Control is
// planned on every conversation refresh, so asking the driver each time would
// be costly. A failed request is not kept: the next one asks again.
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
	definition := a.Agents()[name]
	raw, err := a.driverConfigJSON(definition)
	if err != nil {
		return legacyRuntimeModes()
	}
	key := name + "\x00" + definition.Driver + "\x00" + string(raw)
	if modes, ok := cache.modes[key]; ok {
		return modes
	}
	reporter, ok := a.ConversationBackends[definition.Driver].(RuntimeModesReporter)
	if !ok {
		return legacyRuntimeModes()
	}
	reported, err := reporter.RuntimeModes(ctx, name, raw)
	if err != nil {
		return legacyRuntimeModes()
	}
	cache.modes[key] = reported
	return reported
}

// controlMode chooses the runtime mode that gives a holder its channel: a
// structured control channel for the web, a terminal for tmux. It prefers
// the mode that holder conventionally uses and reports false when no mode
// of the profile offers the channel.
func controlMode(modes driverprotocol.RuntimeModes, holder string) (agent.Mode, bool) {
	want := func(c driverprotocol.ModeCapabilities) bool { return c.Control == driverprotocol.ControlStructured }
	preferred := driverprotocol.ModeHeadless
	switch holder {
	case model.LeaseHolderTerminal:
		want = func(c driverprotocol.ModeCapabilities) bool { return c.Terminal }
		preferred = driverprotocol.ModeTUI
	case model.LeaseHolderCLI:
		want = func(c driverprotocol.ModeCapabilities) bool { return c.Control != "" }
	}
	candidates := modes.ModesWith(want, preferred)
	if len(candidates) == 0 {
		return "", false
	}
	return agent.Mode(candidates[0]), true
}
