package app

import (
	"context"
	"fmt"
	"strconv"

	"github.com/regb/workitem/internal/agent"
	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/coordinator"
	piadapter "github.com/regb/workitem/internal/pi"
	"github.com/regb/workitem/internal/version"
)

func (a *App) AgentExec(ctx context.Context, itemID, sessionID, runtimeID string, mode agent.Mode) error {
	if a.Pi == nil {
		return fmt.Errorf("pi adapter is not configured")
	}
	return a.primaryAgentService().Execute(ctx, itemID, sessionID, runtimeID, mode, func(ctx context.Context, spec agentcore.ExecutionSpec) error {
		return a.Pi.ExecMode(ctx, piadapter.ExecSpec{SessionPath: spec.SessionPath, Mode: spec.Mode, CWD: spec.CWD, LogPath: spec.LogPath, WorkItemID: spec.WorkItemID, RuntimeID: spec.RuntimeID, ControlSocketPath: spec.ControlSocketPath, DaemonSocketPath: a.DaemonSocketPath, Env: map[string]string{"WI_ID": spec.WorkItemID, "WI_DIR": a.Store.ItemDir(spec.WorkItemID), "WI_WORKTREE": spec.CWD, "WI_AGENT_RUNTIME_ID": spec.RuntimeID, "WI_AGENT_MODE": string(spec.Mode), "WI_AGENT_CONTROL_SOCKET": spec.ControlSocketPath, "WI_DAEMON_SOCKET": a.DaemonSocketPath, "WI_DAEMON_PROTOCOL": strconv.Itoa(coordinator.ProtocolVersion), "WI_BUILD_IDENTITY": version.BuildIdentity(), "PI_CODING_AGENT_SESSION_DIR": spec.SessionDir}})
	})
}
