package app

import (
	"context"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app/contract"
	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/model"
)

type AgentControlOptions struct {
	ResolveOptions
	CommandType       string
	Message           string
	Images            []agent.ImageContent
	Actor             string
	RequestID         string
	ExpectedRuntimeID string
	RequireIdle       bool
}

type AgentControlResult = agentcore.ControlResult

func (a *App) AgentControl(ctx context.Context, opts AgentControlOptions) (AgentControlResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts.ResolveOptions)
	if err != nil {
		return AgentControlResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	return a.primaryAgentService().Control(ctx, agentcore.ControlOptions{ResolveOptions: contract.ResolveOptions(opts.ResolveOptions), CommandType: opts.CommandType, Message: opts.Message, Images: opts.Images, Actor: opts.Actor, RequestID: opts.RequestID, ExpectedRuntimeID: opts.ExpectedRuntimeID, RequireIdle: opts.RequireIdle})
}

func (a *App) runtimeControlSocketPath(itemID string, runtime *model.AgentRuntime) string {
	return a.primaryAgentService().ControlSocketPath(itemID, runtime)
}

func (a *App) runtimeLogPath(itemID string, runtime *model.AgentRuntime) string {
	return a.primaryAgentService().LogPath(itemID, runtime)
}
