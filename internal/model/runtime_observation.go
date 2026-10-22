package model

// ProcessInfo is an adapter-neutral process snapshot used by runtime
// observation.
type ProcessInfo struct {
	PID       int
	PPID      int
	PGRP      int
	StartTime uint64
	// LegacyStartTime is the start time identity as recorded before it was
	// read locale-independently, or zero; runtimes started earlier carry it.
	LegacyStartTime uint64
	State           string
	Command         string
	Cmdline         []string
}

// StartedAs reports whether this is the process whose start time identity
// was recorded.
func (i ProcessInfo) StartedAs(recorded uint64) bool {
	return recorded != 0 && (i.StartTime == recorded || i.LegacyStartTime == recorded)
}

func (i ProcessInfo) CommandLine() string {
	if len(i.Cmdline) == 0 {
		return i.Command
	}
	out := ""
	for n, part := range i.Cmdline {
		if n > 0 {
			out += " "
		}
		out += part
	}
	return out
}

// TerminalPaneInfo is an adapter-neutral tmux pane observation.
type TerminalPaneInfo struct {
	SessionName string
	WindowName  string
	PaneID      string
	PaneIndex   string
	PanePID     int
	Command     string
	CurrentPath string
}
