package model

import "time"

// TerminalRuntime caches volatile handles for the optional terminal provider.
// Agent process ownership belongs to AgentRuntime; worktree identity belongs to
// Manifest.Checkout. These handles may go stale and are rebuildable.
type TerminalRuntime struct {
	UpdatedAt       time.Time `json:"updated_at"`
	TmuxWindow      string    `json:"tmux_window,omitempty"`
	TmuxPaneID      string    `json:"tmux_pane_id,omitempty"`
	TmuxPaneIndex   string    `json:"tmux_pane_index,omitempty"`
	TmuxPanePID     int       `json:"tmux_pane_pid,omitempty"`
	TmuxPaneCommand string    `json:"tmux_pane_command,omitempty"`
	TmuxPanePath    string    `json:"tmux_pane_path,omitempty"`
}
