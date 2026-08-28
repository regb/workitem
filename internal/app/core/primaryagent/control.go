package primaryagent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/runtimepath"
)

type ControlOptions struct {
	contract.ResolveOptions
	CommandType string
	Message     string
	Actor       string
	RequestID   string
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
	runtime, err := s.store.LoadAgentRuntime(m.ID)
	if err != nil {
		return ControlResult{}, err
	}
	if !s.ObserveOwnership(runtime).ControlAvailable {
		return ControlResult{}, fmt.Errorf("agent runtime control channel is not active; restart the agent runtime")
	}
	commandType := strings.TrimSpace(opts.CommandType)
	switch commandType {
	case agent.CommandPrompt, agent.CommandSteer, agent.CommandFollowUp:
		if strings.TrimSpace(opts.Message) == "" {
			return ControlResult{}, fmt.Errorf("agent control %s requires a message", commandType)
		}
	case agent.CommandAbort, agent.CommandShutdown:
		if strings.TrimSpace(opts.Message) != "" {
			return ControlResult{}, fmt.Errorf("agent control %s does not accept a message", commandType)
		}
	default:
		return ControlResult{}, fmt.Errorf("unsupported agent control command %q", commandType)
	}
	commandID, err := s.newID()
	if err != nil {
		return ControlResult{}, err
	}
	actor := strings.TrimSpace(opts.Actor)
	if actor == "" {
		actor = "operator"
	}
	command := agent.ControlCommand{ID: commandID, RequestID: strings.TrimSpace(opts.RequestID), Type: commandType, Actor: actor, Message: strings.TrimSpace(opts.Message), CreatedAt: s.now()}
	if err := s.SubmitControl(ctx, m.ID, *runtime, command); err != nil {
		return ControlResult{}, err
	}
	return ControlResult{WorkItemID: m.ID, Runtime: *runtime, Command: command, Submitted: true}, nil
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
	if strings.TrimSpace(s.RuntimeSocketRoot) != "" {
		return filepath.Join(s.RuntimeSocketRoot, filepath.FromSlash(relative))
	}
	return filepath.Join(s.store.ItemDir(itemID), "agent", "control.sock")
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
