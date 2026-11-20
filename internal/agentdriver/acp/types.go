package acp

import "encoding/json"

// Methods and notifications of ACP version 1 used by the client.
const (
	MethodInitialize        = "initialize"
	MethodSessionNew        = "session/new"
	MethodSessionResume     = "session/resume"
	MethodSessionLoad       = "session/load"
	MethodSessionPrompt     = "session/prompt"
	MethodSessionCancel     = "session/cancel"
	MethodSetConfigOption   = "session/set_config_option"
	MethodSessionUpdate     = "session/update"
	MethodRequestPermission = "session/request_permission"
	MethodReadTextFile      = "fs/read_text_file"
	// MethodElicitation asks the client to fill in a form, such as the
	// answers to the agent's questions.
	MethodElicitation = "elicitation/create"
)

// CodeAuthRequired is the error an agent returns until the user signs in.
const CodeAuthRequired = -32000

// CodeMethodNotFound refuses a method the receiver does not implement.
const CodeMethodNotFound = -32601

type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type FileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type ClientCapabilities struct {
	FS       FileSystemCapabilities `json:"fs"`
	Terminal bool                   `json:"terminal"`
	// Elicitation says the client answers form requests, such as questions.
	Elicitation *ElicitationCapabilities `json:"elicitation,omitempty"`
}

type ElicitationCapabilities struct {
	Form struct{} `json:"form"`
}

type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         ClientInfo         `json:"clientInfo"`
}

type PromptCapabilities struct {
	Image           bool `json:"image,omitempty"`
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
}

type SessionCapabilities struct {
	Resume *json.RawMessage `json:"resume,omitempty"`
	List   *json.RawMessage `json:"list,omitempty"`
}

type AgentCapabilities struct {
	LoadSession         bool                `json:"loadSession,omitempty"`
	PromptCapabilities  PromptCapabilities  `json:"promptCapabilities"`
	SessionCapabilities SessionCapabilities `json:"sessionCapabilities"`
	Meta                json.RawMessage     `json:"_meta,omitempty"`
}

type AuthMethod struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []AuthMethod      `json:"authMethods,omitempty"`
	AgentInfo         *ClientInfo       `json:"agentInfo,omitempty"`
}

// NewSessionParams start a session; ResumeSessionParams continue one.
type NewSessionParams struct {
	CWD        string          `json:"cwd"`
	MCPServers []any           `json:"mcpServers"`
	Meta       json.RawMessage `json:"_meta,omitempty"`
}

type ResumeSessionParams struct {
	SessionID  string          `json:"sessionId"`
	CWD        string          `json:"cwd"`
	MCPServers []any           `json:"mcpServers"`
	Meta       json.RawMessage `json:"_meta,omitempty"`
}

// ConfigOption is one session setting, such as the model, with its choices.
// Choices may be grouped.
type ConfigOption struct {
	ID           string         `json:"id"`
	Name         string         `json:"name,omitempty"`
	Category     string         `json:"category,omitempty"`
	Type         string         `json:"type,omitempty"`
	CurrentValue string         `json:"currentValue,omitempty"`
	Options      []ConfigChoice `json:"options,omitempty"`
}

type ConfigChoice struct {
	Value       string         `json:"value,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	GroupID     string         `json:"groupId,omitempty"`
	Options     []ConfigChoice `json:"options,omitempty"`
}

// Values flattens a choice list, groups included.
func Values(choices []ConfigChoice) []ConfigChoice {
	var out []ConfigChoice
	for _, c := range choices {
		if c.GroupID != "" || len(c.Options) > 0 {
			out = append(out, Values(c.Options)...)
			continue
		}
		out = append(out, c)
	}
	return out
}

type SessionResult struct {
	SessionID     string         `json:"sessionId,omitempty"`
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

type SetConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

type SetConfigOptionResult struct {
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

// ContentBlock is prompt or message content.
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// Stop reasons of a prompt.
const (
	StopEndTurn         = "end_turn"
	StopMaxTokens       = "max_tokens"
	StopMaxTurnRequests = "max_turn_requests"
	StopRefusal         = "refusal"
	StopCancelled       = "cancelled"
)

type PromptResult struct {
	StopReason string `json:"stopReason"`
}

type CancelParams struct {
	SessionID string `json:"sessionId"`
}

// SessionUpdateParams carry one session update notification.
type SessionUpdateParams struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

// Session update kinds.
const (
	UpdateAgentMessage  = "agent_message_chunk"
	UpdateAgentThought  = "agent_thought_chunk"
	UpdateUserMessage   = "user_message_chunk"
	UpdateToolCall      = "tool_call"
	UpdateToolCallPatch = "tool_call_update"
	UpdatePlan          = "plan"
	UpdateUsage         = "usage_update"
	UpdateConfigOption  = "config_option_update"
)

// SessionUpdate is one update; fields depend on its kind.
type SessionUpdate struct {
	Kind          string            `json:"sessionUpdate"`
	Content       json.RawMessage   `json:"content,omitempty"`
	ToolCallID    string            `json:"toolCallId,omitempty"`
	Title         *string           `json:"title,omitempty"`
	Name          *string           `json:"name,omitempty"`
	ToolKind      *string           `json:"kind,omitempty"`
	Status        *string           `json:"status,omitempty"`
	RawInput      json.RawMessage   `json:"rawInput,omitempty"`
	RawOutput     json.RawMessage   `json:"rawOutput,omitempty"`
	Meta          json.RawMessage   `json:"_meta,omitempty"`
	ConfigOptions []ConfigOption    `json:"configOptions,omitempty"`
	Entries       []json.RawMessage `json:"entries,omitempty"`
}

// Tool call statuses.
const (
	ToolPending    = "pending"
	ToolInProgress = "in_progress"
	ToolCompleted  = "completed"
	ToolFailed     = "failed"
)

// ToolCallContent is one entry of a tool call's content: content, a diff,
// or a terminal.
type ToolCallContent struct {
	Type    string        `json:"type"`
	Content *ContentBlock `json:"content,omitempty"`
	Path    string        `json:"path,omitempty"`
	OldText *string       `json:"oldText,omitempty"`
	NewText string        `json:"newText,omitempty"`
}

// PermissionOption is a choice the agent offers in a permission request.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// Permission option kinds.
const (
	AllowOnce    = "allow_once"
	AllowAlways  = "allow_always"
	RejectOnce   = "reject_once"
	RejectAlways = "reject_always"
)

type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  SessionUpdate      `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type RequestPermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// Selected answers a permission request with an option.
func Selected(optionID string) RequestPermissionResult {
	return RequestPermissionResult{Outcome: PermissionOutcome{Outcome: "selected", OptionID: optionID}}
}

// Cancelled answers a permission request without choosing.
func Cancelled() RequestPermissionResult {
	return RequestPermissionResult{Outcome: PermissionOutcome{Outcome: "cancelled"}}
}
