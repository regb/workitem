package driverprotocol

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// OperationRuntimeModes asks what each runtime mode of a profile offers.
const OperationRuntimeModes = "runtime.modes"

// RuntimeModes describes, without launching anything, the runtime modes a
// driver offers for one profile configuration. It is stateless: it may check
// the installed backend version but must not start an agent or create native
// content.
//
// Contract: every listed mode can resume any conversation of this driver with
// the same profile settings. That is what lets wi replace a runtime with one
// in another mode on the same conversation, for example when a client needs a
// control channel the running mode lacks.
//
// The answer is an expectation. A runtime's Ready capabilities remain
// authoritative, and wi checks them after launch.
type RuntimeModes struct {
	Modes map[Mode]ModeCapabilities `json:"modes"`
	// Settings lists profile settings (such as "model") that may differ
	// between successive runtimes of one conversation.
	Settings map[string]SettingChange `json:"settings,omitempty"`
}

// OperationConfigModels asks for the models and reasoning levels a profile
// can use. Like config.validate it is stateless; a driver may ask its backend
// for its catalog but must not start an agent session or make model requests.
const OperationConfigModels = "config.models"

// The settings an item can choose for its agent, as configuration keys.
// Reasoning is one ladder of depth levels for every driver; each driver maps
// it onto its backend's own setting.
const (
	SettingModel     = "model"
	SettingReasoning = "reasoning"
)

// ReasoningLevels is the reasoning ladder, shallowest first. A driver may
// accept only some levels, and a model fewer still.
var ReasoningLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// ModelCatalog lists the models a profile can use and the reasoning levels
// its driver accepts.
type ModelCatalog struct {
	ReasoningLevels []string      `json:"reasoning_levels,omitempty"`
	Models          []ModelOption `json:"models"`
	// Default is the model the agent uses when no model is set, and
	// DefaultReasoning its reasoning level when none is set, when the driver
	// knows them.
	Default          string `json:"default,omitempty"`
	DefaultReasoning string `json:"default_reasoning,omitempty"`
}

// ModelOption is one selectable model. Value is what the model setting
// takes. Levels, when set, narrows the reasoning levels for this model; an
// empty list with Reasoning false means it has no reasoning setting.
type ModelOption struct {
	Value string `json:"value"`
	// Resolved is the model Value currently names, when Value is an alias.
	Resolved    string   `json:"resolved,omitempty"`
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
	Reasoning   bool     `json:"reasoning"`
	Levels      []string `json:"levels,omitempty"`
	ImageInput  bool     `json:"image_input,omitempty"`
}

// ValidSettingValues checks reported setting values: only the model and a
// reasoning level from the ladder, as short single-line text.
func ValidSettingValues(values map[string]string) error {
	if len(values) == 0 {
		return fmt.Errorf("no setting values")
	}
	for name, value := range values {
		if name != SettingModel && name != SettingReasoning {
			return fmt.Errorf("unknown setting %q", name)
		}
		if value == "" || len(value) > 200 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("invalid %s value", name)
		}
		if name == SettingReasoning && !slices.Contains(ReasoningLevels, value) {
			return fmt.Errorf("invalid reasoning level %q", value)
		}
	}
	return nil
}

// ModeCapabilities are the channels a runtime in one mode offers.
type ModeCapabilities struct {
	// Control is ControlStructured, ControlEmulated, or empty when the mode
	// accepts no input from wi.
	Control string `json:"control,omitempty"`
	// Terminal is a native TUI that a person can attach to.
	Terminal   bool `json:"terminal,omitempty"`
	IdleGuard  bool `json:"idle_guard,omitempty"`
	ImageInput bool `json:"image_input,omitempty"`
}

// SettingChange says when a profile setting can change on one conversation:
// when a new runtime resumes it, and in which modes while a runtime runs.
type SettingChange struct {
	OnResume bool   `json:"on_resume"`
	Live     []Mode `json:"live,omitempty"`
}

// ModesWith returns the modes satisfying want, preferred first, then the rest
// in name order.
func (m RuntimeModes) ModesWith(want func(ModeCapabilities) bool, preferred ...Mode) []Mode {
	result := []Mode{}
	seen := map[Mode]bool{}
	for _, mode := range preferred {
		if capabilities, ok := m.Modes[mode]; ok && !seen[mode] && want(capabilities) {
			result = append(result, mode)
			seen[mode] = true
		}
	}
	rest := []Mode{}
	for mode, capabilities := range m.Modes {
		if !seen[mode] && want(capabilities) {
			rest = append(rest, mode)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i] < rest[j] })
	return append(result, rest...)
}

// Validate rejects answers wi cannot rely on: no modes, unknown modes, or an
// unknown control channel.
func (m RuntimeModes) Validate() error {
	if len(m.Modes) == 0 {
		return fmt.Errorf("runtime.modes lists no modes")
	}
	for mode, capabilities := range m.Modes {
		if mode != ModeTUI && mode != ModeHeadless {
			return fmt.Errorf("runtime.modes lists unknown mode %q", mode)
		}
		switch capabilities.Control {
		case "", ControlStructured, ControlEmulated:
		default:
			return fmt.Errorf("runtime.modes mode %s has unknown control %q", mode, capabilities.Control)
		}
	}
	for name, setting := range m.Settings {
		for _, mode := range setting.Live {
			if _, ok := m.Modes[mode]; !ok {
				return fmt.Errorf("runtime.modes setting %s is live in unlisted mode %q", name, mode)
			}
		}
	}
	return nil
}
