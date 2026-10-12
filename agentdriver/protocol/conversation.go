package driverprotocol

import (
	"encoding/json"
	"time"
)

const (
	OperationLaunch              = "launch"
	OperationConversationCreate  = "conversation.create"
	OperationConversationCWD     = "conversation.cwd"
	OperationConversationFork    = "conversation.fork"
	OperationConversationObserve = "conversation.observe"
	OperationConversationDecode  = "conversation.decode"
)

type Request struct {
	ProtocolVersion int                  `json:"protocol_version"`
	Driver          string               `json:"driver"`
	Operation       string               `json:"operation"`
	Agent           string               `json:"agent,omitempty"`
	Config          json.RawMessage      `json:"config,omitempty"`
	Launch          *LaunchRequest       `json:"launch,omitempty"`
	Conversation    *ConversationRequest `json:"conversation,omitempty"`
}

type Response struct {
	ProtocolVersion int                   `json:"protocol_version"`
	Ready           *Ready                `json:"ready,omitempty"`
	Conversation    *ConversationResponse `json:"conversation,omitempty"`
	Error           string                `json:"error,omitempty"`
}

type ConversationRequest struct {
	ID           string       `json:"id,omitempty"`
	Conversation Conversation `json:"conversation,omitempty"`
	Path         string       `json:"path,omitempty"`
	SourcePath   string       `json:"source_path,omitempty"`
	TargetPath   string       `json:"target_path,omitempty"`
	TargetCWD    string       `json:"target_cwd,omitempty"`
	Now          time.Time    `json:"now,omitempty"`
	StartLine    uint64       `json:"start_line,omitempty"`
	LineNumbers  []uint64     `json:"line_numbers,omitempty"`
	Lines        [][]byte     `json:"lines,omitempty"`
}

type ConversationResponse struct {
	Conversation Conversation             `json:"conversation,omitempty"`
	CWD          string                   `json:"cwd,omitempty"`
	Observation  *ConversationObservation `json:"observation,omitempty"`
	Facts        []ConversationFact       `json:"facts,omitempty"`
	Decoded      []bool                   `json:"decoded,omitempty"`
	Warnings     []string                 `json:"warnings,omitempty"`
}

type ConversationObservation struct {
	Source                 string            `json:"source,omitempty"`
	Exists                 bool              `json:"exists"`
	SizeBytes              int64             `json:"size_bytes,omitempty"`
	ModTime                time.Time         `json:"mod_time,omitempty"`
	EntriesScanned         int               `json:"entries_scanned"`
	MalformedLines         int               `json:"malformed_lines,omitempty"`
	LastEvent              *ConversationFact `json:"last_event,omitempty"`
	LastTurnActivity       *ConversationFact `json:"last_turn_activity,omitempty"`
	LastUserPrompt         *ConversationFact `json:"last_user_prompt,omitempty"`
	LastAssistantMessage   *ConversationFact `json:"last_assistant_message,omitempty"`
	LastTerminalAssistant  *ConversationFact `json:"last_terminal_assistant,omitempty"`
	LastToolActivity       *ConversationFact `json:"last_tool_activity,omitempty"`
	LastActivityAgeSeconds int64             `json:"last_activity_age_seconds,omitempty"`
	InferredTurnState      string            `json:"inferred_turn_state,omitempty"`
	UnavailableReason      string            `json:"unavailable_reason,omitempty"`
}

type ConversationFact struct {
	Line         uint64    `json:"line"`
	Type         string    `json:"type,omitempty"`
	Role         string    `json:"role,omitempty"`
	Timestamp    time.Time `json:"timestamp,omitempty"`
	StopReason   string    `json:"stop_reason,omitempty"`
	ContentTypes []string  `json:"content_types,omitempty"`
	Terminal     bool      `json:"terminal,omitempty"`
	Failed       bool      `json:"failed,omitempty"`
}
