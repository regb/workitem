package agent

import (
	"fmt"
	"strings"
)

type Mode string

const (
	ModeTUI Mode = "tui"
	ModeRPC Mode = "rpc"
)

func ParseMode(value string) (Mode, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case ModeTUI, ModeRPC:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid agent runtime mode %q; expected tui or rpc", value)
	}
}

func (m Mode) Valid() bool {
	return m == ModeTUI || m == ModeRPC
}

type Capabilities struct {
	NativeTUI bool `json:"native_tui"`
	Headless  bool `json:"headless"`
	Streaming bool `json:"streaming"`
	Steering  bool `json:"steering"`
	FollowUp  bool `json:"follow_up"`
	Cancel    bool `json:"cancel"`
	Monitor   bool `json:"monitor"`
}

func CapabilitiesForMode(mode Mode) Capabilities {
	switch mode {
	case ModeTUI:
		return Capabilities{NativeTUI: true, Streaming: true, Steering: true, FollowUp: true, Cancel: true, Monitor: true}
	case ModeRPC:
		return Capabilities{Headless: true, Streaming: true, Steering: true, FollowUp: true, Cancel: true, Monitor: true}
	default:
		return Capabilities{}
	}
}
