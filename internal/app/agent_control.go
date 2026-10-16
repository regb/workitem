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
	CommandType        string
	Message            string
	Images             []agent.ImageContent
	Actor              string
	RequestID          string
	ExpectedRuntimeID  string
	RequireIdle        bool
	ControllerHolder   string
	ControllerClientID string
	ControllerLeaseID  string
}

type AgentControlResult = agentcore.ControlResult

func (a *App) AgentControl(ctx context.Context, opts AgentControlOptions) (AgentControlResult, error) {
	m, err := a.ResolveItem(ctx, opts.ResolveOptions)
	if err != nil {
		return AgentControlResult{}, err
	}
	unlock, err := a.lockController(ctx, m.ID)
	if err != nil {
		return AgentControlResult{}, err
	}
	defer unlock()
	opts.Selector = m.ID
	return a.agentControlWithLease(ctx, opts)
}

// The caller holds the controller lock before acquiring the resource operation
// lock. Takeover uses this path to abort without recursively taking either lock.
func (a *App) agentControlWithLease(ctx context.Context, opts AgentControlOptions) (AgentControlResult, error) {
	m, operation, err := a.lockItemOperation(ctx, opts.ResolveOptions)
	if err != nil {
		return AgentControlResult{}, err
	}
	defer operation.Release()
	opts.Selector = m.ID
	release, err := a.admitControllerCommand(ctx, m.ID, opts)
	if err != nil {
		return AgentControlResult{}, err
	}
	defer release()
	return a.primaryAgentService().Control(ctx, agentcore.ControlOptions{ResolveOptions: contract.ResolveOptions(opts.ResolveOptions), CommandType: opts.CommandType, Message: opts.Message, Images: opts.Images, Actor: opts.Actor, RequestID: opts.RequestID, ExpectedRuntimeID: opts.ExpectedRuntimeID, RequireIdle: opts.RequireIdle})
}

func (a *App) runtimeControlSocketPath(itemID string, runtime *model.AgentRuntime) string {
	return a.primaryAgentService().ControlSocketPath(itemID, runtime)
}

func (a *App) runtimeLogPath(itemID string, runtime *model.AgentRuntime) string {
	return a.primaryAgentService().LogPath(itemID, runtime)
}
