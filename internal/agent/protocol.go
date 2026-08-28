package agent

import (
	"encoding/json"
	"time"
)

const RuntimeProtocolVersion = 1

const (
	CommandPrompt   = "prompt"
	CommandSteer    = "steer"
	CommandFollowUp = "follow_up"
	CommandAbort    = "abort"
	CommandShutdown = "shutdown"
)

type ControlCommand struct {
	ProtocolVersion int       `json:"protocol_version"`
	ID              string    `json:"id"`
	RuntimeID       string    `json:"runtime_id"`
	WorkItemID      string    `json:"work_item_id"`
	RequestID       string    `json:"request_id,omitempty"`
	Type            string    `json:"type"`
	Actor           string    `json:"actor,omitempty"`
	Message         string    `json:"message,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
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

type RuntimeEvent struct {
	ProtocolVersion  int             `json:"protocol_version"`
	EventID          string          `json:"event_id,omitempty"`
	RuntimeID        string          `json:"runtime_id"`
	WorkItemID       string          `json:"work_item_id"`
	Type             string          `json:"type"`
	Timestamp        time.Time       `json:"timestamp"`
	CommandID        string          `json:"command_id,omitempty"`
	RequestID        string          `json:"request_id,omitempty"`
	Role             string          `json:"role,omitempty"`
	Text             string          `json:"text,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ToolName         string          `json:"tool_name,omitempty"`
	Error            string          `json:"error,omitempty"`
	Data             map[string]any  `json:"data,omitempty"`
	Backend          string          `json:"backend,omitempty"`
	BackendEventType string          `json:"backend_event_type,omitempty"`
	BackendEvent     json.RawMessage `json:"backend_event,omitempty"`
}
