package app

import (
	"context"

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
	return a.primaryAgentService().Control(ctx, agentcore.ControlOptions{ResolveOptions: contract.ResolveOptions(opts.ResolveOptions), CommandType: opts.CommandType, Message: opts.Message, Actor: opts.Actor, RequestID: opts.RequestID})
}

func (a *App) runtimeControlSocketPath(itemID string, runtime *model.AgentRuntime) string {
	return a.primaryAgentService().ControlSocketPath(itemID, runtime)
}

func (a *App) runtimeLogPath(itemID string, runtime *model.AgentRuntime) string {
	return a.primaryAgentService().LogPath(itemID, runtime)
}
