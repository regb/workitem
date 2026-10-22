package model

import "time"

const (
	AgentRuntimeStarting = "starting"
	AgentRuntimeRunning  = "running"
	AgentRuntimeStopping = "stopping"
	AgentRuntimeStopped  = "stopped"
	AgentRuntimeProblem  = "problem"
)

// AgentRuntime records the rebuildable local process/control-plane handles for
// the primary agent. The durable conversation identity remains in the manifest.
type RuntimeCapabilities struct {
	LiveContent bool `json:"live_content,omitempty"`
	IdleGuard   bool `json:"idle_guard,omitempty"`
	ImageInput  bool `json:"image_input,omitempty"`
	NativeTUI   bool `json:"native_tui"`
	Headless    bool `json:"headless"`
	Streaming   bool `json:"streaming"`
	Prompt      bool `json:"prompt"`
	Steering    bool `json:"steering"`
	FollowUp    bool `json:"follow_up"`
	Cancel      bool `json:"cancel"`
	Monitor     bool `json:"monitor"`
	// Control is the driver-reported control channel kind; see ControlChannel.
	Control string `json:"control,omitempty"`
	// Settings are the profile settings this runtime can change live.
	Settings []string `json:"settings,omitempty"`
}

// LiveSetting reports whether the runtime can change a setting while running.
func (c RuntimeCapabilities) LiveSetting(name string) bool {
	for _, setting := range c.Settings {
		if setting == name {
			return true
		}
	}
	return false
}

type AgentRuntime struct {
	ID                       string              `json:"id"`
	WorkItemID               string              `json:"work_item_id"`
	Mode                     string              `json:"mode"`
	State                    string              `json:"state"`
	ConversationID           string              `json:"conversation_id"`
	Executor                 string              `json:"executor,omitempty"`
	WorkspaceClaimID         string              `json:"workspace_claim_id,omitempty"`
	WorkspaceClaimGeneration uint64              `json:"workspace_claim_generation,omitempty"`
	Capabilities             RuntimeCapabilities `json:"capabilities"`
	HostPID                  int                 `json:"host_pid,omitempty"`
	HostProcessGroup         int                 `json:"host_process_group,omitempty"`
	HostStartTime            uint64              `json:"host_start_time,omitempty"`
	StartedAt                time.Time           `json:"started_at"`
	UpdatedAt                time.Time           `json:"updated_at"`
	StoppedAt                *time.Time          `json:"stopped_at,omitempty"`
	Readiness                string              `json:"readiness,omitempty"`
	ReadinessReasonCode      string              `json:"readiness_reason_code,omitempty"`
	ReadinessObservedAt      *time.Time          `json:"readiness_observed_at,omitempty"`
}
