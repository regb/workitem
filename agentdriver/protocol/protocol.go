// Package driverprotocol defines the versioned wire contract between wi and
// external coding-agent drivers. It intentionally has no dependencies on wi's
// internal packages so driver implementations may import it directly.
package driverprotocol

import "encoding/json"

// Version identifies the process bootstrap and conversation-operation protocol.
// Version 2 transports native conversation lines as opaque base64 bytes.
const Version = 2

const BootstrapSocketEnv = "WI_AGENT_DRIVER_BOOTSTRAP_SOCKET"

type Mode string

const (
	ModeTUI      Mode = "tui"
	ModeHeadless Mode = "headless"
)

type Conversation struct {
	ID      string `json:"id"`
	Locator string `json:"locator"`
	Format  string `json:"format"`
}

type Runtime struct {
	ID                string `json:"id"`
	WorkItemID        string `json:"work_item_id"`
	Mode              Mode   `json:"mode"`
	CWD               string `json:"cwd"`
	ControlSocketPath string `json:"control_socket_path"`
	EventSocketPath   string `json:"event_socket_path"`
	LogPath           string `json:"log_path,omitempty"`
}

type LaunchRequest struct {
	ProtocolVersion int               `json:"protocol_version"`
	Driver          string            `json:"driver"`
	Agent           string            `json:"agent"`
	Runtime         Runtime           `json:"runtime"`
	Conversation    Conversation      `json:"conversation"`
	Config          json.RawMessage   `json:"config"`
	Environment     map[string]string `json:"environment,omitempty"`
	Skills          []Skill           `json:"skills,omitempty"`
}

type Capabilities struct {
	LiveContent   bool `json:"live_content,omitempty"`
	IdleGuard     bool `json:"idle_guard,omitempty"`
	ImageInput    bool `json:"image_input,omitempty"`
	BundledSkills bool `json:"bundled_skills,omitempty"`
	NativeTUI     bool `json:"native_tui"`
	Headless      bool `json:"headless"`
	Streaming     bool `json:"streaming"`
	Prompt        bool `json:"prompt"`
	Steering      bool `json:"steering"`
	FollowUp      bool `json:"follow_up"`
	Cancel        bool `json:"cancel"`
	Monitor       bool `json:"monitor"`
}

type Hello struct {
	ProtocolVersion int    `json:"protocol_version"`
	Driver          string `json:"driver"`
	DriverVersion   string `json:"driver_version,omitempty"`
}

type Ready struct {
	ProtocolVersion int          `json:"protocol_version"`
	Capabilities    Capabilities `json:"capabilities"`
	BackendPath     string       `json:"backend_path,omitempty"`
	BackendVersion  string       `json:"backend_version,omitempty"`
	Error           string       `json:"error,omitempty"`
}
