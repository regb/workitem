package primaryagent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/runtimepath"
)

type ControlOptions struct {
	contract.ResolveOptions
	CommandType       string
	Message           string
	Images            []agent.ImageContent
	Actor             string
	RequestID         string
	ExpectedRuntimeID string
	RequireIdle       bool
}

type ControlResult struct {
	WorkItemID string               `json:"work_item_id"`
	Runtime    model.AgentRuntime   `json:"runtime"`
	Command    agent.ControlCommand `json:"command"`
	Submitted  bool                 `json:"submitted"`
}

func (s *Service) Control(ctx context.Context, opts ControlOptions) (ControlResult, error) {
	m, err := s.resolve(ctx, opts.ResolveOptions)
	if err != nil {
		return ControlResult{}, err
	}
	commandType := strings.TrimSpace(opts.CommandType)
	if m.State == model.StateArchived && commandType != agent.CommandAbort && commandType != agent.CommandShutdown {
		return ControlResult{}, fmt.Errorf("archived conversations are read-only")
	}
	runtime, err := s.store.LoadAgentRuntime(m.ID)
	if err != nil {
		return ControlResult{}, err
	}
	if !s.ObserveOwnership(runtime).ControlAvailable {
		return ControlResult{}, fmt.Errorf("agent runtime control channel is not active; restart the agent runtime")
	}
	if opts.ExpectedRuntimeID != "" && runtime.ID != opts.ExpectedRuntimeID {
		return ControlResult{}, fmt.Errorf("runtime owner changed; reload runtime status before submitting")
	}
	if opts.RequireIdle && !runtime.Capabilities.IdleGuard {
		return ControlResult{}, fmt.Errorf("agent driver does not support idle-only control")
	}
	if len(opts.Images) > 0 && !runtime.Capabilities.ImageInput {
		return ControlResult{}, fmt.Errorf("agent driver does not support image input")
	}
	// Stopping a stale owner must remain possible, but it cannot accept new work.
	if commandType != agent.CommandAbort && commandType != agent.CommandShutdown && runtime.WorkspaceClaimID != "" {
		claim := m.Checkout.Claim
		if claim == nil || claim.ID != runtime.WorkspaceClaimID || claim.Generation != runtime.WorkspaceClaimGeneration {
			return ControlResult{}, fmt.Errorf("agent runtime %s belongs to a superseded workspace claim", runtime.ID)
		}
	}
	switch commandType {
	case agent.CommandPrompt, agent.CommandSteer, agent.CommandFollowUp:
		if strings.TrimSpace(opts.Message) == "" && len(opts.Images) == 0 {
			return ControlResult{}, fmt.Errorf("agent control %s requires a message or image", commandType)
		}
	case agent.CommandAbort, agent.CommandShutdown:
		if strings.TrimSpace(opts.Message) != "" || len(opts.Images) != 0 {
			return ControlResult{}, fmt.Errorf("agent control %s does not accept message content", commandType)
		}
	default:
		return ControlResult{}, fmt.Errorf("unsupported agent control command %q", commandType)
	}
	if !supportsControl(runtime.Capabilities, commandType) {
		return ControlResult{}, fmt.Errorf("agent driver does not support %s control", commandType)
	}
	commandID, err := s.newID()
	if err != nil {
		return ControlResult{}, err
	}
	actor := strings.TrimSpace(opts.Actor)
	if actor == "" {
		actor = "operator"
	}
	command := agent.ControlCommand{ID: commandID, RequestID: strings.TrimSpace(opts.RequestID), Type: commandType, Actor: actor, Message: strings.TrimSpace(opts.Message), Images: opts.Images, CreatedAt: s.now(), RequireIdle: opts.RequireIdle}
	if err := s.SubmitControl(ctx, m.ID, *runtime, command); err != nil {
		return ControlResult{}, err
	}
	return ControlResult{WorkItemID: m.ID, Runtime: *runtime, Command: command, Submitted: true}, nil
}

func supportsControl(capabilities model.RuntimeCapabilities, commandType string) bool {
	switch commandType {
	case agent.CommandPrompt:
		return capabilities.Prompt
	case agent.CommandSteer:
		return capabilities.Steering
	case agent.CommandFollowUp:
		return capabilities.FollowUp
	case agent.CommandAbort:
		return capabilities.Cancel
	case agent.CommandShutdown:
		return true
	default:
		return false
	}
}

func (s *Service) SubmitControl(ctx context.Context, itemID string, runtime model.AgentRuntime, command agent.ControlCommand) error {
	command.ProtocolVersion = agent.RuntimeProtocolVersion
	command.RuntimeID = runtime.ID
	command.WorkItemID = itemID
	socketPath := s.ControlSocketPath(itemID, &runtime)
	if socketPath == "" {
		return fmt.Errorf("agent runtime %s has no control socket; restart the agent runtime", runtime.ID)
	}
	err := agent.SubmitControlSocket(ctx, socketPath, command)
	if err != nil {
		if errors.Is(err, agent.ErrControlSocketUnavailable) {
			return fmt.Errorf("agent runtime control socket is unavailable; restart the agent runtime: %w", err)
		}
		return err
	}
	return nil
}

func (s *Service) ControlSocketPath(itemID string, runtime *model.AgentRuntime) string {
	if runtime == nil || strings.TrimSpace(runtime.ID) == "" {
		return ""
	}
	relative := runtimepath.ControlSocket(itemID, runtime.ID)
	namespace := strings.TrimSpace(s.RuntimeSocketRoot)
	var socketPath string
	if namespace != "" {
		socketPath = filepath.Join(namespace, filepath.FromSlash(relative))
	} else {
		namespace = s.store.ItemDir(itemID)
		socketPath = filepath.Join(namespace, "agent", "control.sock")
	}
	return portableRuntimeSocketPath(goruntime.GOOS, socketPath, namespace, relative)
}

func (s *Service) EventSocketPath(itemID string, runtime *model.AgentRuntime) string {
	if runtime == nil || strings.TrimSpace(runtime.ID) == "" {
		return ""
	}
	relative := runtimepath.EventSocket(itemID, runtime.ID)
	namespace := strings.TrimSpace(s.RuntimeSocketRoot)
	var socketPath string
	if namespace != "" {
		socketPath = filepath.Join(namespace, filepath.FromSlash(relative))
	} else {
		namespace = s.store.ItemDir(itemID)
		socketPath = filepath.Join(namespace, "agent", "events.sock")
	}
	return portableRuntimeSocketPath(goruntime.GOOS, socketPath, namespace, relative)
}

func portableRuntimeSocketPath(goos, candidate, namespace, relative string) string {
	if goos != "darwin" || len(candidate) < 100 {
		return candidate
	}
	digest := sha256.Sum256([]byte(filepath.Clean(namespace)))
	rootKey := fmt.Sprintf("%x", digest[:6])
	return filepath.Join("/tmp", fmt.Sprintf("wi-%d", os.Getuid()), "runtime", rootKey, filepath.FromSlash(relative))
}

func (s *Service) LogPath(itemID string, runtime *model.AgentRuntime) string {
	if runtime == nil || strings.TrimSpace(runtime.ID) == "" {
		return ""
	}
	relative := runtimepath.DiagnosticLog(itemID, runtime.ID)
	if strings.TrimSpace(s.RuntimeStateRoot) != "" {
		return filepath.Join(s.RuntimeStateRoot, filepath.FromSlash(relative))
	}
	return filepath.Join(s.store.ItemDir(itemID), "agent", "runtimes", runtime.ID, "runtime.log")
}
