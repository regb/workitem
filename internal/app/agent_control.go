package app

import (
	"context"
	"encoding/json"

	"github.com/regb/workitem/internal/app/contract"
	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/model"
)

type AgentControlOptions struct {
	ResolveOptions
	CommandType string
	Message     string
	Actor       string
	RequestID   string
}

type AgentControlResult = agentcore.ControlResult

func (a *App) AgentControl(ctx context.Context, opts AgentControlOptions) (AgentControlResult, error) {
	manifest, err := a.ResolveItem(ctx, opts.ResolveOptions)
	if err != nil {
		return AgentControlResult{}, err
	}
	if manifest.Checkout.Specification.Executor != "" && manifest.Checkout.Specification.Executor != model.ExecutorLocal {
		client, err := a.executorClient(manifest.Checkout.Specification.Executor)
		if err != nil {
			return AgentControlResult{}, err
		}
		remote, err := client.RuntimeControl(ctx, manifest.ID, opts.CommandType, opts.Message)
		if err != nil {
			return AgentControlResult{}, err
		}
		var result AgentControlResult
		if err := json.Unmarshal(remote.ControlJSON, &result); err != nil {
			return AgentControlResult{}, err
		}
		result.Runtime.Executor = manifest.Checkout.Specification.Executor
		result.Runtime.HostPID, result.Runtime.HostProcessGroup, result.Runtime.HostStartTime = 0, 0, 0
		_ = a.Store.SaveAgentRuntime(ctx, manifest.ID, result.Runtime)
		_ = a.ingestExecutorEvents(ctx, remote.EventsJSON)
		_ = a.checkpointExecutorConversation(ctx, manifest, remote.Conversation)
		return result, nil
	}
	return a.primaryAgentService().Control(ctx, agentcore.ControlOptions{ResolveOptions: contract.ResolveOptions(opts.ResolveOptions), CommandType: opts.CommandType, Message: opts.Message, Actor: opts.Actor, RequestID: opts.RequestID})
}

func (a *App) runtimeControlSocketPath(itemID string, runtime *model.AgentRuntime) string {
	return a.primaryAgentService().ControlSocketPath(itemID, runtime)
}

func (a *App) runtimeLogPath(itemID string, runtime *model.AgentRuntime) string {
	return a.primaryAgentService().LogPath(itemID, runtime)
}
