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
}
