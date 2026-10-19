package agent

import (
	"encoding/json"
	"time"

	driverprotocol "github.com/regb/workitem/agentdriver/protocol"
)

const RuntimeProtocolVersion = driverprotocol.RuntimeVersion

const (
	CommandPrompt   = driverprotocol.CommandPrompt
	CommandSteer    = driverprotocol.CommandSteer
	CommandFollowUp = driverprotocol.CommandFollowUp
	CommandAbort    = driverprotocol.CommandAbort
	CommandShutdown = driverprotocol.CommandShutdown
)

type ControlCommand = driverprotocol.ControlCommand
type ImageContent = driverprotocol.ImageContent

const (
	EventRuntimeStatus    = driverprotocol.EventRuntimeStatus
	EventRuntimeReady     = driverprotocol.EventRuntimeReady
	EventCommandAccepted  = driverprotocol.EventCommandAccepted
	EventCommandRejected  = driverprotocol.EventCommandRejected
	EventAgentStarted     = driverprotocol.EventAgentStarted
	EventAgentSettled     = driverprotocol.EventAgentSettled
	EventTurnStarted      = driverprotocol.EventTurnStarted
	EventTurnEnded        = driverprotocol.EventTurnEnded
	EventMessageDelta     = driverprotocol.EventMessageDelta
	EventMessageCompleted = driverprotocol.EventMessageCompleted
	EventToolStarted      = driverprotocol.EventToolStarted
	EventToolUpdated      = driverprotocol.EventToolUpdated
	EventToolCompleted    = driverprotocol.EventToolCompleted
	EventQueueChanged     = driverprotocol.EventQueueChanged
	EventSessionChanged   = driverprotocol.EventSessionChanged
	EventRuntimeStopping  = driverprotocol.EventRuntimeStopping
	EventRuntimeFailed    = driverprotocol.EventRuntimeFailed
)

type RuntimeEvent struct {
	ProtocolVersion int       `json:"protocol_version"`
	EventID         string    `json:"event_id,omitempty"`
	RuntimeID       string    `json:"runtime_id"`
	WorkItemID      string    `json:"work_item_id"`
	Type            string    `json:"type"`
	Timestamp       time.Time `json:"timestamp"`
	CommandID       string    `json:"command_id,omitempty"`
	RequestID       string    `json:"request_id,omitempty"`
	Role            string    `json:"role,omitempty"`
	Text            string    `json:"text,omitempty"`
	ToolCallID      string    `json:"tool_call_id,omitempty"`
	ToolName        string    `json:"tool_name,omitempty"`
	Outcome         string    `json:"outcome,omitempty"`
	// AgentState is a native current-state observation, not inferred settlement.
	AgentState       string          `json:"agent_state,omitempty"`
	LeafID           string          `json:"leaf_id,omitempty"`
	Error            string          `json:"error,omitempty"`
	Failed           bool            `json:"failed,omitempty"`
	Readiness        string          `json:"readiness,omitempty"`
	ReasonCode       string          `json:"reason_code,omitempty"`
	DeliveryState    string          `json:"delivery_state,omitempty"`
	Data             map[string]any  `json:"data,omitempty"`
	Backend          string          `json:"backend,omitempty"`
	BackendEventType string          `json:"backend_event_type,omitempty"`
	BackendEvent     json.RawMessage `json:"backend_event,omitempty"`
}
