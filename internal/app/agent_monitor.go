package app

import (
	"context"
	"time"

	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/model"
)

type AgentStatusOptions struct {
	ResolveOptions
	StaleAfter time.Duration
}
type AgentStatusResult = agentcore.AgentStatusResult
type AgentProcessStatus = agentcore.AgentProcessStatus
type PiSessionStatus = agentcore.PiSessionStatus
type PiSessionEventSummary = agentcore.PiSessionEventSummary
type WorktreeStatus = agentcore.WorktreeStatus

func (a *App) AgentStatus(ctx context.Context, opts AgentStatusOptions) (AgentStatusResult, error) {
	return a.primaryAgentService().AgentStatus(ctx, agentcore.AgentStatusOptions{ResolveOptions: opts.ResolveOptions, StaleAfter: opts.StaleAfter})
}
func (a *App) recordPrimaryTerminalRuntime(ctx context.Context, m model.Manifest, window string) []string {
	return a.primaryAgentService().RecordPrimaryTerminalRuntime(ctx, m, window)
}
func (a *App) inspectPiSession(m model.Manifest, now time.Time) (PiSessionStatus, []string) {
	if a.PiSessionObserver != nil {
		return a.PiSessionObserver(m, now)
	}
	return a.primaryAgentService().InspectPiSession(m, now)
}
