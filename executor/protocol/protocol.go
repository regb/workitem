// Package protocol defines the public protocol between a wi coordinator and
// an execution worker. It intentionally excludes coordinator database access.
package protocol

const Version = 1

const (
	OperationDoctor           = "doctor"
	OperationAuthorityInfo    = "authority.info"
	OperationWorkspaceEnsure  = "workspace.ensure"
	OperationWorkspaceStatus  = "workspace.status"
	OperationWorkspaceRelease = "workspace.release"
	OperationRuntimeEnsure    = "runtime.ensure"
	OperationRuntimeStatus    = "runtime.status"
	OperationRuntimeControl   = "runtime.control"
	OperationTerminalClose    = "terminal.close"
	OperationShutdown         = "shutdown"
)

type Request struct {
	ProtocolVersion int               `json:"protocol_version"`
	Operation       string            `json:"operation"`
	MasterID        string            `json:"master_id"`
	ExecutorName    string            `json:"executor_name,omitempty"`
	Terminal        *TerminalProfile  `json:"terminal,omitempty"`
	Workspace       *WorkspaceRequest `json:"workspace,omitempty"`
	Runtime         *RuntimeRequest   `json:"runtime,omitempty"`
}

type WorkspaceRequest struct {
	ItemID            string `json:"item_id"`
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

type RuntimeRequest struct {
	ItemID      string `json:"item_id"`
	Mode        string `json:"mode,omitempty"`
	CommandType string `json:"command_type,omitempty"`
	Message     string `json:"message,omitempty"`
}

type TerminalProfile struct {
	Shell       string            `json:"shell,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

type Response struct {
	ProtocolVersion int              `json:"protocol_version"`
	Doctor          *DoctorResult    `json:"doctor,omitempty"`
	Authority       *AuthorityResult `json:"authority,omitempty"`
	Workspace       *WorkspaceResult `json:"workspace,omitempty"`
	Runtime         *RuntimeResult   `json:"runtime,omitempty"`
	Error           string           `json:"error,omitempty"`
}

type AuthorityResult struct {
	SocketPath string `json:"socket_path"`
}

type WorkspaceResult struct {
	Path         string `json:"path"`
	Slot         string `json:"slot"`
	Source       string `json:"source"`
	StatusJSON   []byte `json:"status_json,omitempty"`
	ManifestJSON []byte `json:"manifest_json,omitempty"`
	Changed      bool   `json:"changed,omitempty"`
	GitBundle    []byte `json:"git_bundle,omitempty"`
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
