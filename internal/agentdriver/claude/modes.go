package claude

import protocol "github.com/regb/workitem/agentdriver/protocol"

// RuntimeModes reports Claude Code's modes. Both resume the same transcript
// with --resume. The TUI accepts input only as keystrokes into its tmux pane;
// headless (Claude Code 2.1.299 or later) takes structured JSON input. Model
// and reasoning (Claude's effort) change on resume through launch flags, and live in headless mode
// through Claude's control requests; the TUI would need typed slash commands.
// Ready still reports what the installed version supports.
func RuntimeModes() protocol.RuntimeModes {
	return protocol.RuntimeModes{
		Modes: map[protocol.Mode]protocol.ModeCapabilities{
			protocol.ModeTUI:      {Control: protocol.ControlEmulated, Terminal: true, ImageInput: true},
			protocol.ModeHeadless: {Control: protocol.ControlStructured, ImageInput: true},
		},
		Settings: map[string]protocol.SettingChange{
			protocol.SettingModel:     {OnResume: true, Live: []protocol.Mode{protocol.ModeHeadless}},
			protocol.SettingReasoning: {OnResume: true, Live: []protocol.Mode{protocol.ModeHeadless}},
		},
	}
}
