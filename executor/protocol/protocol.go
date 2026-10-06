// Package protocol defines the public protocol between a wi coordinator and
// an execution worker. It intentionally excludes coordinator database access.
package protocol

const Version = 1

const (
	OperationDoctor           = "doctor"
	OperationItemDelete       = "item.delete"
	OperationItemsReconcile   = "items.reconcile"
	OperationAuthorityInfo    = "authority.info"
	OperationWorkspaceEnsure  = "workspace.ensure"
	OperationWorkspaceStatus  = "workspace.status"
	OperationWorkspaceRelease = "workspace.release"
	OperationWorkspaceExec    = "workspace.exec"
	OperationRuntimeEnsure    = "runtime.ensure"
	OperationRuntimeStatus    = "runtime.status"
	OperationRuntimeControl   = "runtime.control"
	OperationTerminalClose    = "terminal.close"
	OperationTerminalCopyMode = "terminal.copy_mode"
	OperationShutdown         = "shutdown"
)

type Request struct {
	ProtocolVersion int               `json:"protocol_version"`
	Operation       string            `json:"operation"`
	MasterID        string            `json:"master_id"`
	ExecutorName    string            `json:"executor_name,omitempty"`
	Terminal        *TerminalProfile  `json:"terminal,omitempty"`
	Item            *ItemRequest      `json:"item,omitempty"`
	Items           *ItemsRequest     `json:"items,omitempty"`
	Workspace       *WorkspaceRequest `json:"workspace,omitempty"`
	Tool            *ToolRequest      `json:"tool,omitempty"`
	Runtime         *RuntimeRequest   `json:"runtime,omitempty"`
}

type ItemRequest struct {
	ItemID string `json:"item_id"`
}

type ItemsRequest struct {
	KeepIDs []string `json:"keep_ids"`
}

type WorkspaceRequest struct {
	ItemID            string `json:"item_id"`
	ClaimID           string `json:"claim_id,omitempty"`
	ClaimGeneration   uint64 `json:"claim_generation,omitempty"`
	ExecutorName      string `json:"executor_name,omitempty"`
	Description       string `json:"description,omitempty"`
	RepositoryKey     string `json:"repository_key"`
	CreatedFromCommit string `json:"created_from_commit"`
	Branch            string `json:"branch"`
	Slot              string `json:"slot"`
	ManifestJSON      []byte `json:"manifest_json"`
	GitBundle         []byte `json:"git_bundle"`
	Config            string `json:"config"`
	Force             bool   `json:"force,omitempty"`
}

type ToolRequest struct {
	ItemID          string            `json:"item_id"`
	ExecutorName    string            `json:"executor_name"`
	ClaimID         string            `json:"claim_id"`
	ClaimGeneration uint64            `json:"claim_generation"`
	RepositoryKey   string            `json:"repository_key"`
	Slot            string            `json:"slot"`
	CWD             string            `json:"cwd,omitempty"`
	Command         []string          `json:"command"`
	Stdin           []byte            `json:"stdin,omitempty"`
	Environment     map[string]string `json:"environment,omitempty"`
	TimeoutMS       int64             `json:"timeout_ms,omitempty"`
}

type RuntimeRequest struct {
	ItemID       string `json:"item_id"`
	AttachmentID string `json:"attachment_id,omitempty"`
	Mode         string `json:"mode,omitempty"`
	CommandType  string `json:"command_type,omitempty"`
	Message      string `json:"message,omitempty"`
}

type TerminalProfile struct {
	Shell       string            `json:"shell,omitempty"`
	ModeKeys    string            `json:"mode_keys,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

type Response struct {
	ProtocolVersion int              `json:"protocol_version"`
	Doctor          *DoctorResult    `json:"doctor,omitempty"`
	Authority       *AuthorityResult `json:"authority,omitempty"`
	Item            *ItemResult      `json:"item,omitempty"`
	Items           *ItemsResult     `json:"items,omitempty"`
	Workspace       *WorkspaceResult `json:"workspace,omitempty"`
	Tool            *ToolResult      `json:"tool,omitempty"`
	Runtime         *RuntimeResult   `json:"runtime,omitempty"`
	Error           string           `json:"error,omitempty"`
}

type AuthorityResult struct {
	SocketPath string `json:"socket_path"`
}

type ItemResult struct {
	Changed bool `json:"changed"`
}

type ItemsResult struct {
	RemovedIDs []string `json:"removed_ids"`
	BlockedIDs []string `json:"blocked_ids"`
}

type WorkspaceResult struct {
	ClaimID         string `json:"claim_id,omitempty"`
	ClaimGeneration uint64 `json:"claim_generation,omitempty"`
	Executor        string `json:"executor,omitempty"`
	RepositoryKey   string `json:"repository_key,omitempty"`
	Path            string `json:"path"`
	Slot            string `json:"slot"`
	Source          string `json:"source"`
	StatusJSON      []byte `json:"status_json,omitempty"`
	ManifestJSON    []byte `json:"manifest_json,omitempty"`
	Changed         bool   `json:"changed,omitempty"`
	GitBundle       []byte `json:"git_bundle,omitempty"`
}

type ToolResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    []byte `json:"stdout,omitempty"`
	Stderr    []byte `json:"stderr,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type RuntimeResult struct {
	RuntimeJSON  []byte                `json:"runtime_json,omitempty"`
	ManifestJSON []byte                `json:"manifest_json,omitempty"`
	ControlJSON  []byte                `json:"control_json,omitempty"`
	Changed      bool                  `json:"changed,omitempty"`
	StatusJSON   []byte                `json:"status_json,omitempty"`
	EventsJSON   []byte                `json:"events_json,omitempty"`
	Conversation *ConversationSnapshot `json:"conversation,omitempty"`
}

type ConversationSnapshot struct {
	Driver  string `json:"driver"`
	ID      string `json:"id"`
	Locator string `json:"locator"`
	Format  string `json:"format,omitempty"`
	Data    []byte `json:"data"`
}

type DoctorResult struct {
	WorkerID      string            `json:"worker_id"`
	Version       string            `json:"version"`
	BuildIdentity string            `json:"build_identity"`
	GOOS          string            `json:"goos"`
	GOARCH        string            `json:"goarch"`
	Home          string            `json:"home"`
	Tools         map[string]string `json:"tools"`
	Tmux          TmuxProbe         `json:"tmux"`
}

type TmuxProbe struct {
	SocketPath   string `json:"socket_path"`
	ConfigPath   string `json:"config_path"`
	DefaultShell string `json:"default_shell,omitempty"`
}
