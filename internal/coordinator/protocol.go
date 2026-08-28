package coordinator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/regb/workitem/internal/dataroot"
	"github.com/regb/workitem/internal/model"
)

const (
	ProtocolVersion  = 3
	MaxRequestBytes  = 1 << 20
	MaxResponseBytes = 8 << 20
)

const (
	AccessOperator = "operator"
	AccessAgent    = "agent"
)

const (
	MethodPing              = "ping"
	MethodStatus            = "status"
	MethodShutdown          = "shutdown"
	MethodListManifests     = "list_manifests"
	MethodPiSession         = "pi_session"
	MethodAgentObservation  = "agent_observation"
	MethodCanonicalManifest = "canonical_manifest"
	MethodManifestCommand   = "manifest_command"
	MethodManifestQueue     = "manifest_command_queue"
	MethodCreateItem        = "create_item"
	MethodStoreCommand      = "store_command"
	MethodRuntimeEvent      = "runtime_event"
	MethodRuntimeEvents     = "runtime_events"
	MethodItemEvents        = "item_events"
	MethodActivityBarrier   = "activity_barrier"
	MethodActionability     = "actionability_snapshot"
	MethodItemResources     = "item_resources"
)

type Request struct {
	ProtocolVersion int             `json:"protocol_version"`
	BuildIdentity   string          `json:"build_identity,omitempty"`
	ID              string          `json:"id"`
	Method          string          `json:"method"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	ProtocolVersion int             `json:"protocol_version"`
	BuildIdentity   string          `json:"build_identity,omitempty"`
	ID              string          `json:"id"`
	OK              bool            `json:"ok"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *ProtocolError  `json:"error,omitempty"`
}

type CompatibilityError struct {
	Kind     string
	Expected string
	Actual   string
}

func (e *CompatibilityError) Error() string {
	return fmt.Sprintf("daemon %s mismatch: client %s, daemon %s", e.Kind, e.Expected, e.Actual)
}

type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type ProjectionMetadata struct {
	Revision   uint64    `json:"revision"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
	Fresh      bool      `json:"fresh"`
	Warnings   []string  `json:"warnings,omitempty"`
}

type ItemRequest struct {
	ItemID string `json:"item_id"`
}

type ItemEventsResult struct {
	Events []model.Event `json:"events"`
}

type ItemResourcesResult struct {
	Runtime  *model.AgentRuntime    `json:"runtime,omitempty"`
	Terminal *model.TerminalRuntime `json:"terminal,omitempty"`
}

type PiSessionProjectionResult struct {
	Projection ProjectionMetadata `json:"projection"`
	Found      bool               `json:"found"`
	Session    PiSessionIndex     `json:"session"`
}

type ManifestProjectionResult struct {
	Projection ProjectionMetadata `json:"projection"`
	Manifests  []model.Manifest   `json:"manifests"`
}

type ActivityBarrierResult struct {
	Projection ProjectionMetadata `json:"projection"`
	PiImport   PiImportReport     `json:"pi_import"`
}

type ActionabilitySnapshotRequest struct {
	CurrentItemID string                    `json:"current_item_id,omitempty"`
	Queue         ActionabilityQueueOptions `json:"queue"`
}

type ActionabilitySnapshotResult struct {
	Projection ProjectionMetadata       `json:"projection"`
	PiImport   PiImportReport           `json:"pi_import"`
	Queue      ActionabilityQueueResult `json:"queue"`
	Selection  ActionabilitySelection   `json:"selection"`
}

type Status struct {
	ProtocolVersion int            `json:"protocol_version"`
	BuildIdentity   string         `json:"build_identity"`
	PID             int            `json:"pid"`
	StartedAt       string         `json:"started_at"`
	DataRoot        string         `json:"data_root"`
	SocketPath      string         `json:"socket_path"`
	AgentSocketPath string         `json:"agent_socket_path,omitempty"`
	Access          string         `json:"access"`
	Database        DatabaseStatus `json:"database"`
	PiImport        PiImportReport `json:"pi_import"`
}

func SocketPath(runtimeDir, dataRoot string) (string, error) {
	if strings.TrimSpace(runtimeDir) == "" || strings.TrimSpace(dataRoot) == "" {
		return "", errors.New("runtime directory and data root are required")
	}
	key, err := dataroot.Key(dataRoot)
	if err != nil {
		return "", err
	}
	name := key + ".sock"
	return portableSocketPath(filepath.Join(runtimeDir, "wi", name), "wi", name), nil
}

// AgentSocketPath is the restricted control-plane endpoint intended for
// sandboxed agent tools. Its directory is separate so containers can mount it
// without exposing the operator socket.
func AgentSocketPath(runtimeDir, dataRoot string) (string, error) {
	if strings.TrimSpace(runtimeDir) == "" || strings.TrimSpace(dataRoot) == "" {
		return "", errors.New("runtime directory and data root are required")
	}
	key, err := dataroot.Key(dataRoot)
	if err != nil {
		return "", err
	}
	return portableSocketPath(filepath.Join(runtimeDir, "wi-agent", key, "daemon.sock"), "wi-agent", key, "daemon.sock"), nil
}

func portableSocketPath(candidate string, parts ...string) string {
	// Darwin limits Unix socket addresses to 104 bytes. Its default temporary
	// directory is often already long enough to consume most of that budget.
	if runtime.GOOS == "darwin" && len(candidate) >= 100 {
		base := filepath.Join("/tmp", fmt.Sprintf("wi-%d", os.Getuid()))
		return filepath.Join(append([]string{base}, parts...)...)
	}
	return candidate
}
