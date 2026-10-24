package driverprotocol

import (
	"encoding/json"
	"time"
)

const (
	OperationConfigValidate           = "config.validate"
	OperationConfigDiscover           = "config.discover"
	OperationLaunch                   = "launch"
	OperationConversationCreate       = "conversation.create"
	OperationConversationCWD          = "conversation.cwd"
	OperationConversationFork         = "conversation.fork"
	OperationConversationObserve      = "conversation.observe"
	OperationConversationDecode       = "conversation.decode"
	OperationConversationHistory      = "conversation.history"
	OperationConversationCapabilities = "conversation.capabilities"
	OperationConversationTitle        = "conversation.title"
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
	ProtocolVersion  int                     `json:"protocol_version"`
	Ready            *Ready                  `json:"ready,omitempty"`
	Conversation     *ConversationResponse   `json:"conversation,omitempty"`
	ConfigValidation *ConfigValidationResult `json:"config_validation,omitempty"`
	ConfigDiscovery  *ConfigDiscoveryResult  `json:"config_discovery,omitempty"`
	RuntimeModes     *RuntimeModes           `json:"runtime_modes,omitempty"`
	ModelCatalog     *ModelCatalog           `json:"model_catalog,omitempty"`
	Error            string                  `json:"error,omitempty"`
}

// ConfigDiscoveryResult contains installed agent profiles, not credentials.
// Discovery is read-only and must not launch an agent or create native content.
// Config contains a flat object of strings, booleans, integers or string arrays,
// matching Wi's configuration file values. An empty list means not installed.
type ConfigDiscoveryResult struct {
	Profiles []DiscoveredAgentConfig `json:"profiles"`
	Warnings []string                `json:"warnings,omitempty"`
}
type DiscoveredAgentConfig struct {
	Name   string          `json:"name"`
	Label  string          `json:"label,omitempty"`
	Config json.RawMessage `json:"config"`
}

// ConfigValidationResult acknowledges validation of the merged driver config.
// Validation is stateless: it must not launch a backend or create native content.
type ConfigValidationResult struct {
	Valid bool `json:"valid"`
}

type ConversationRequest struct {
	Prompt   string `json:"prompt,omitempty"`
	BeforeID string `json:"before_id,omitempty"`
	// Subagent selects that subagent's conversation for history instead of
	// the main one.
	Subagent     string       `json:"subagent,omitempty"`
	LeafID       string       `json:"leaf_id,omitempty"`
	ID           string       `json:"id,omitempty"`
	Conversation Conversation `json:"conversation,omitempty"`
	Path         string       `json:"path,omitempty"`
	SourcePath   string       `json:"source_path,omitempty"`
	TargetPath   string       `json:"target_path,omitempty"`
	TargetCWD    string       `json:"target_cwd,omitempty"`
	Now          time.Time    `json:"now,omitempty"`
	StartLine    uint64       `json:"start_line,omitempty"`
	LineNumbers  []uint64     `json:"line_numbers,omitempty"`
	// Native records are opaque bytes, including malformed JSONL lines. JSON
	// encodes each line as base64 rather than parsing it as protocol content.
	Lines [][]byte `json:"lines,omitempty"`
}

// ConversationCapabilities describes optional stateless operations. It can be
// queried without creating a conversation or launching the backend agent.
type ConversationCapabilities struct {
	HistoryPaging bool `json:"history_paging,omitempty"`
	History       bool `json:"history"`
	Title         bool `json:"title"`
	// Subagents means history can read a subagent's conversation.
	Subagents bool `json:"subagents,omitempty"`
}

type ConversationResponse struct {
	Capabilities *ConversationCapabilities `json:"capabilities,omitempty"`
	Title        string                    `json:"title,omitempty"`
	History      *History                  `json:"history,omitempty"`
	Conversation Conversation              `json:"conversation,omitempty"`
	CWD          string                    `json:"cwd,omitempty"`
	Observation  *ConversationObservation  `json:"observation,omitempty"`
	Facts        []ConversationFact        `json:"facts,omitempty"`
	Decoded      []bool                    `json:"decoded,omitempty"`
	Warnings     []string                  `json:"warnings,omitempty"`
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
