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
type AgentRuntime struct {
	ID               string     `json:"id"`
	WorkItemID       string     `json:"work_item_id"`
	Mode             string     `json:"mode"`
	State            string     `json:"state"`
	ConversationID   string     `json:"conversation_id"`
	HostPID          int        `json:"host_pid,omitempty"`
	HostProcessGroup int        `json:"host_process_group,omitempty"`
	HostStartTime    uint64     `json:"host_start_time,omitempty"`
	StartedAt        time.Time  `json:"started_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	StoppedAt        *time.Time `json:"stopped_at,omitempty"`
}
