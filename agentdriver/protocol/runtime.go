package driverprotocol

import (
	"time"
)

const (
	CommandPrompt   = "prompt"
	CommandSteer    = "steer"
	CommandFollowUp = "follow_up"
	CommandAbort    = "abort"
	CommandShutdown = "shutdown"
)

// RuntimeVersion is independent of process bootstrap so conversation transport
// changes do not invalidate the control/event sockets of running agents.
const RuntimeVersion = 1

type ImageContent struct {
	MIMEType string `json:"mime_type"`
	Data     string `json:"data"`
}

type ControlCommand struct {
	ProtocolVersion int            `json:"protocol_version"`
	ID              string         `json:"id"`
	RuntimeID       string         `json:"runtime_id"`
	WorkItemID      string         `json:"work_item_id"`
	RequestID       string         `json:"request_id,omitempty"`
	Type            string         `json:"type"`
	Actor           string         `json:"actor,omitempty"`
	Message         string         `json:"message,omitempty"`
	Images          []ImageContent `json:"images,omitempty"`
	RequireIdle     bool           `json:"require_idle,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

const (
	EventRuntimeReady     = "runtime.ready"
	EventCommandAccepted  = "command.accepted"
	EventCommandRejected  = "command.rejected"
	EventAgentStarted     = "agent.started"
	EventAgentSettled     = "agent.settled"
	EventTurnStarted      = "turn.started"
	EventTurnEnded        = "turn.ended"
	EventMessageDelta     = "message.delta"
	EventMessageCompleted = "message.completed"
	EventToolStarted      = "tool.started"
	EventToolUpdated      = "tool.updated"
	EventToolCompleted    = "tool.completed"
	EventQueueChanged     = "queue.changed"
	EventSessionChanged   = "session.changed"
	EventRuntimeStopping  = "runtime.stopping"
	EventRuntimeFailed    = "runtime.failed"
)

// ValidAgentObservation validates the optional compact native-state snapshot.
// The leaf is an opaque entry token; "-" denotes an empty branch.
func ValidAgentObservation(state, leaf, readiness string) bool {
	if state == "" && leaf == "" {
		return true
	}
	if len(leaf) == 0 || len(leaf) > 200 {
		return false
	}
	for _, c := range leaf {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	switch state {
	case "active":
		return readiness == "busy"
	case "idle", "failed":
		return readiness == "ready"
	default:
		return false
	}
}

type RuntimeEvent struct {
	ProtocolVersion int       `json:"protocol_version"`
	EventID         string    `json:"event_id"`
	RuntimeID       string    `json:"runtime_id"`
	WorkItemID      string    `json:"work_item_id"`
	Type            string    `json:"type"`
	Timestamp       time.Time `json:"timestamp"`
	CommandID       string    `json:"command_id,omitempty"`
	RequestID       string    `json:"request_id,omitempty"`
	Role            string    `json:"role,omitempty"`
	ToolName        string    `json:"tool_name,omitempty"`
	Failed          bool      `json:"failed,omitempty"`
	Readiness       string    `json:"readiness,omitempty"`
	ReasonCode      string    `json:"reason_code,omitempty"`
	DeliveryState   string    `json:"delivery_state,omitempty"`

	// Outcome is explicit final settlement: completed or failed. An absent
	// outcome from older drivers must not be mistaken for proven success.
	Outcome string `json:"outcome,omitempty"`
	// AgentState is a native current-state observation, not inferred settlement.
	AgentState string `json:"agent_state,omitempty"`
	LeafID     string `json:"leaf_id,omitempty"`
}
