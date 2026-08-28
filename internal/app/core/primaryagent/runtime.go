package primaryagent

import (
	"context"
	"errors"
	"fmt"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app/contract"
	"github.com/regb/workitem/internal/model"
)

type StopResult struct {
	WorkItemID string              `json:"work_item_id"`
	Changed    bool                `json:"changed"`
	Runtime    *model.AgentRuntime `json:"runtime,omitempty"`
	Warnings   []string            `json:"warnings"`
}

func (s *Service) Prepare(ctx context.Context, m model.Manifest, session model.PiSession, mode agent.Mode) (model.AgentRuntime, error) {
	if !mode.Valid() {
		return model.AgentRuntime{}, fmt.Errorf("invalid agent runtime mode %q", mode)
	}
	if existing, err := s.store.LoadAgentRuntime(m.ID); err != nil {
		return model.AgentRuntime{}, err
	} else if s.ObserveOwnership(existing).ProcessAlive {
		return model.AgentRuntime{}, fmt.Errorf("agent runtime %s is already active in %s mode; stop it before starting %s mode", existing.ID, existing.Mode, mode)
	}
	now := s.now()
	runtimeID := fmt.Sprintf("%s-%d", mode, now.UnixNano())
	runtime := model.AgentRuntime{ID: runtimeID, WorkItemID: m.ID, Mode: string(mode), State: model.AgentRuntimeStarting, ConversationID: session.ID, StartedAt: now, UpdatedAt: now}
	if err := s.store.SaveAgentRuntime(ctx, m.ID, runtime); err != nil {
		return model.AgentRuntime{}, err
	}
	_ = s.store.AppendEvent(ctx, m.ID, model.NewEvent(now, "agent_runtime.prepared", "wi", map[string]any{"runtime_id": runtime.ID, "mode": runtime.Mode, "conversation_id": runtime.ConversationID}))
	return runtime, nil
}

func (s *Service) Stop(ctx context.Context, opts contract.ResolveOptions, force bool) (StopResult, error) {
	m, err := s.resolve(ctx, opts)
	if err != nil {
		return StopResult{}, err
	}
	runtime, err := s.store.LoadAgentRuntime(m.ID)
	if err != nil {
		return StopResult{}, err
	}
	result := StopResult{WorkItemID: m.ID, Runtime: runtime, Warnings: []string{}}
	ownership := s.ObserveOwnership(runtime)
	if !ownership.ProcessAlive {
		return result, nil
	}
	if !force && s.busy != nil {
		busy, err := s.busy(ctx, m.ID)
		if err != nil {
			return result, err
		}
		if busy {
			return result, fmt.Errorf("primary agent is busy; wait for it to settle or pass --force to abort before stopping")
		}
	}
	now := s.now()
	if force {
		abort := agent.ControlCommand{ID: fmt.Sprintf("abort-%d", now.UnixNano()), Type: agent.CommandAbort, Actor: "operator", CreatedAt: now}
		if err := s.SubmitControl(ctx, m.ID, *runtime, abort); err != nil {
			if errors.Is(err, agent.ErrControlSocketUnavailable) && ownership.IdentityVerified {
				return s.terminateRuntimeGroup(ctx, m.ID, runtime, result, "control socket unavailable; terminated verified agent runtime process group")
			}
			return result, err
		}
	}
	shutdown := agent.ControlCommand{ID: fmt.Sprintf("shutdown-%d", now.UnixNano()), Type: agent.CommandShutdown, Actor: "operator", CreatedAt: now}
	if err := s.SubmitControl(ctx, m.ID, *runtime, shutdown); err != nil {
		if force && errors.Is(err, agent.ErrControlSocketUnavailable) && ownership.IdentityVerified {
			return s.terminateRuntimeGroup(ctx, m.ID, runtime, result, "control socket unavailable; terminated verified agent runtime process group")
		}
		return result, err
	}
	runtime.State, runtime.UpdatedAt = model.AgentRuntimeStopping, now
	if err := s.store.SaveAgentRuntime(ctx, m.ID, *runtime); err != nil {
		return result, err
	}
	result.Changed, result.Runtime = true, runtime
	return result, nil
}

func (s *Service) TerminateVerified(ctx context.Context, itemID string, runtime *model.AgentRuntime, warning string) (StopResult, error) {
	result := StopResult{WorkItemID: itemID, Runtime: runtime, Warnings: []string{}}
	ownership := s.ObserveOwnership(runtime)
	if !ownership.ProcessAlive {
		return result, nil
	}
	if runtime.WorkItemID != itemID {
		return result, fmt.Errorf("agent runtime %s belongs to work item %s, not %s", runtime.ID, runtime.WorkItemID, itemID)
	}
	if !ownership.IdentityVerified {
		return result, fmt.Errorf("refusing to signal agent runtime %s because its process identity cannot be verified", runtime.ID)
	}
	return s.terminateRuntimeGroup(ctx, itemID, runtime, result, warning)
}

// terminateRuntimeGroup force-stops a verified runtime owner whose control channel failed.
func (s *Service) terminateRuntimeGroup(ctx context.Context, itemID string, runtime *model.AgentRuntime, result StopResult, warning string) (StopResult, error) {
	if err := s.process.TerminateGroup(runtime.HostPID); err != nil {
		return result, fmt.Errorf("force-stop agent runtime %s: %w", runtime.ID, err)
	}
	now := s.now()
	runtime.State, runtime.UpdatedAt = model.AgentRuntimeStopping, now
	if err := s.store.SaveAgentRuntime(ctx, itemID, *runtime); err != nil {
		return result, err
	}
	result.Changed, result.Runtime = true, runtime
	result.Warnings = append(result.Warnings, warning)
	return result, nil
}
