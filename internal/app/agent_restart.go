package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/regb/workitem/internal/model"
)

// RestartAgentResult reports a restarted agent and the mode it runs in.
type RestartAgentResult struct {
	WorkItemID string `json:"work_item_id"`
	Mode       string `json:"mode"`
}

// RestartAgent stops an item's idle agent and resumes the same conversation
// in the same mode and checkout, without setting the checkout up again. The
// new runtime starts with the item's current settings and the repository's
// current variables.
func (a *App) RestartAgent(ctx context.Context, opts ResolveOptions) (RestartAgentResult, error) {
	m, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return RestartAgentResult{}, err
	}
	if m.State != model.StateActive {
		return RestartAgentResult{}, fmt.Errorf("work item %s is not active", m.ID)
	}
	runtime, err := a.Store.LoadAgentRuntime(m.ID)
	if err != nil || !a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		return RestartAgentResult{}, errors.New("no agent is running; it uses the current settings when it next starts")
	}
	if err := a.restartRuntime(ctx, m, runtime, opts); err != nil {
		return RestartAgentResult{}, err
	}
	return RestartAgentResult{WorkItemID: m.ID, Mode: runtime.Mode}, nil
}

// RunningAgent is an agent running for one of a repository's items.
type RunningAgent struct {
	WorkItemID string    `json:"work_item_id"`
	Title      string    `json:"title"`
	Mode       string    `json:"mode"`
	StartedAt  time.Time `json:"started_at"`
	Busy       bool      `json:"busy"`
}

// RepositoryAgents lists the agents running for a repository's active
// items, oldest first.
func (a *App) RepositoryAgents(ctx context.Context, repositoryID string) []RunningAgent {
	manifests, _ := a.Store.ListManifests()
	agents := []RunningAgent{}
	for _, m := range manifests {
		if m.Repository.ID != repositoryID || m.State != model.StateActive {
			continue
		}
		runtime, err := a.Store.LoadAgentRuntime(m.ID)
		if err != nil || !a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
			continue
		}
		agents = append(agents, RunningAgent{WorkItemID: m.ID, Title: m.Title, Mode: runtime.Mode, StartedAt: runtime.StartedAt, Busy: a.AgentBusy(ctx, m.ID)})
	}
	slices.SortFunc(agents, func(x, y RunningAgent) int {
		if c := x.StartedAt.Compare(y.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(x.WorkItemID, y.WorkItemID)
	})
	return agents
}
