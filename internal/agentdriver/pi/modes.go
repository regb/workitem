package pi

import driverprotocol "github.com/regb/workitem/agentdriver/protocol"

// liveSettings are changed in a running Pi: through RPC set_model and
// set_thinking_level in headless mode, and through wi's extension in the TUI.
var liveSettings = []string{driverprotocol.SettingModel, driverprotocol.SettingReasoning}

// RuntimeModes reports Pi's modes. Both resume the same session file with
// structured control: the TUI through wi's extension, headless through RPC.
// Model and reasoning level change on resume through launch flags, and live
// in both modes.
func RuntimeModes() driverprotocol.RuntimeModes {
	return driverprotocol.RuntimeModes{
		Modes: map[driverprotocol.Mode]driverprotocol.ModeCapabilities{
			driverprotocol.ModeTUI:      {Control: driverprotocol.ControlStructured, Terminal: true, IdleGuard: true, ImageInput: true},
			driverprotocol.ModeHeadless: {Control: driverprotocol.ControlStructured, IdleGuard: true, ImageInput: true},
		},
		Settings: map[string]driverprotocol.SettingChange{
			driverprotocol.SettingModel:     {OnResume: true, Live: []driverprotocol.Mode{driverprotocol.ModeTUI, driverprotocol.ModeHeadless}},
			driverprotocol.SettingReasoning: {OnResume: true, Live: []driverprotocol.Mode{driverprotocol.ModeTUI, driverprotocol.ModeHeadless}},
		},
	}
}
