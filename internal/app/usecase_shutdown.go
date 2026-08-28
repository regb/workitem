package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	"github.com/regb/workitem/internal/model"
)

type ShutdownItemResult struct {
	WorkItemID           string   `json:"work_item_id"`
	RuntimeID            string   `json:"runtime_id,omitempty"`
	RuntimeStopRequested bool     `json:"runtime_stop_requested,omitempty"`
	RuntimeStopped       bool     `json:"runtime_stopped"`
	TerminalSession      string   `json:"terminal_session,omitempty"`
	TerminalClosed       bool     `json:"terminal_closed,omitempty"`
	Warnings             []string `json:"warnings,omitempty"`
}

type ShutdownFailure struct {
	WorkItemID string `json:"work_item_id,omitempty"`
	Resource   string `json:"resource"`
	Error      string `json:"error"`
}

type ShutdownResult struct {
	Items                   []ShutdownItemResult `json:"items"`
	OrphanedTerminalsClosed []string             `json:"orphaned_terminals_closed,omitempty"`
	Failures                []ShutdownFailure    `json:"failures"`
}

func (r ShutdownResult) Complete() bool { return len(r.Failures) == 0 }

type pendingRuntimeShutdown struct {
	itemIndex int
	runtime   *model.AgentRuntime
}

// ShutdownAll stops every canonical runtime, closes canonical and provably
// wi-owned orphaned tmux sessions, and leaves daemon shutdown to the CLI.
func (a *App) ShutdownAll(ctx context.Context, env map[string]string, force bool) (ShutdownResult, error) {
	result := ShutdownResult{Items: []ShutdownItemResult{}, Failures: []ShutdownFailure{}}
	manifests, listErrors := a.Store.ListManifests()
	if len(listErrors) > 0 {
		for _, err := range listErrors {
			if err != nil {
				result.Failures = append(result.Failures, ShutdownFailure{Resource: "inventory", Error: err.Error()})
			}
		}
		return result, fmt.Errorf("list work items before shutdown")
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })

	service := a.primaryAgentService()
	pending := map[string]pendingRuntimeShutdown{}
	protectedSessions := map[string]bool{}
	for _, manifest := range manifests {
		itemIndex := len(result.Items)
		item := ShutdownItemResult{
			WorkItemID:      manifest.ID,
			RuntimeStopped:  true,
			TerminalSession: strings.TrimSpace(manifest.TerminalSessionName()),
		}
		result.Items = append(result.Items, item)
		if item.TerminalSession != "" {
			protectedSessions[item.TerminalSession] = true
		}

		runtime, err := a.Store.LoadAgentRuntime(manifest.ID)
		if err != nil {
			result.Items[itemIndex].RuntimeStopped = false
			result.Failures = append(result.Failures, ShutdownFailure{WorkItemID: manifest.ID, Resource: "runtime", Error: err.Error()})
			continue
		}
		if runtime == nil {
			continue
		}
		result.Items[itemIndex].RuntimeID = runtime.ID
		if !service.ObserveOwnership(runtime).ProcessAlive {
			continue
		}
		result.Items[itemIndex].RuntimeStopped = false
		stopped, err := a.StopAgentRuntime(ctx, ResolveOptions{Selector: manifest.ID, Env: env}, force)
		if err != nil {
			result.Failures = append(result.Failures, ShutdownFailure{WorkItemID: manifest.ID, Resource: "runtime", Error: err.Error()})
			continue
		}
		result.Items[itemIndex].RuntimeStopRequested = stopped.Changed
		result.Items[itemIndex].Warnings = append(result.Items[itemIndex].Warnings, stopped.Warnings...)
		pending[manifest.ID] = pendingRuntimeShutdown{itemIndex: itemIndex, runtime: runtime}
	}

	timeout := a.ShutdownRuntimeStopTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	waitForRuntimeShutdowns(ctx, service, pending, timeout)
	if force {
		for itemID, entry := range pending {
			if !service.ObserveOwnership(entry.runtime).ProcessAlive {
				continue
			}
			terminated, err := service.TerminateVerified(ctx, itemID, entry.runtime, "graceful shutdown timed out; terminated verified agent runtime process group")
			if err != nil {
				result.Failures = append(result.Failures, ShutdownFailure{WorkItemID: itemID, Resource: "runtime", Error: err.Error()})
				continue
			}
			result.Items[entry.itemIndex].Warnings = append(result.Items[entry.itemIndex].Warnings, terminated.Warnings...)
		}
		waitForRuntimeShutdowns(ctx, service, pending, 2*time.Second)
	}
	for itemID, entry := range pending {
		alive := service.ObserveOwnership(entry.runtime).ProcessAlive
		result.Items[entry.itemIndex].RuntimeStopped = !alive
		if alive {
			result.Failures = append(result.Failures, ShutdownFailure{WorkItemID: itemID, Resource: "runtime", Error: "agent runtime is still active after shutdown timeout"})
		}
	}

	for index := range result.Items {
		item := &result.Items[index]
		if !item.RuntimeStopped {
			result.Failures = append(result.Failures, ShutdownFailure{WorkItemID: item.WorkItemID, Resource: "terminal", Error: "terminal was not closed because its agent runtime is still active"})
			continue
		}
		if a.Tmux != nil && item.TerminalSession != "" {
			exists, err := a.Tmux.HasSession(ctx, item.TerminalSession)
			if err != nil {
				result.Failures = append(result.Failures, ShutdownFailure{WorkItemID: item.WorkItemID, Resource: "terminal", Error: "inspect tmux session ownership: " + err.Error()})
				continue
			}
			if exists {
				owned, err := a.provablyOwnedTmuxSession(ctx, item.TerminalSession, item.WorkItemID, false)
				if err != nil {
					result.Failures = append(result.Failures, ShutdownFailure{WorkItemID: item.WorkItemID, Resource: "terminal", Error: "inspect tmux session ownership: " + err.Error()})
					continue
				}
				if !owned {
					result.Failures = append(result.Failures, ShutdownFailure{WorkItemID: item.WorkItemID, Resource: "terminal", Error: "refusing to close tmux session because its wi ownership environment does not match"})
					continue
				}
			}
		}
		closed, err := a.CloseTerminal(ctx, ResolveOptions{Selector: item.WorkItemID, Env: env})
		if err != nil {
			result.Failures = append(result.Failures, ShutdownFailure{WorkItemID: item.WorkItemID, Resource: "terminal", Error: err.Error()})
			continue
		}
		item.TerminalClosed = closed.Changed
		item.Warnings = append(item.Warnings, closed.Warnings...)
	}

	if lister, ok := a.Tmux.(interface {
		ListSessions(context.Context) ([]string, error)
	}); ok {
		sessions, err := lister.ListSessions(ctx)
		if err != nil {
			result.Failures = append(result.Failures, ShutdownFailure{Resource: "tmux inventory", Error: err.Error()})
		} else {
			for _, session := range sessions {
				if protectedSessions[session] {
					continue
				}
				owned, err := a.provablyOwnedTmuxSession(ctx, session, "", true)
				if err != nil {
					result.Failures = append(result.Failures, ShutdownFailure{Resource: "tmux session " + session, Error: err.Error()})
					continue
				}
				if !owned {
					continue
				}
				if err := a.Tmux.KillSession(ctx, session); err != nil {
					result.Failures = append(result.Failures, ShutdownFailure{Resource: "tmux session " + session, Error: err.Error()})
					continue
				}
				result.OrphanedTerminalsClosed = append(result.OrphanedTerminalsClosed, session)
			}
		}
	}
	return result, nil
}

func waitForRuntimeShutdowns(ctx context.Context, service *agentcore.Service, pending map[string]pendingRuntimeShutdown, timeout time.Duration) {
	if len(pending) == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for {
		allStopped := true
		for _, entry := range pending {
			if service.ObserveOwnership(entry.runtime).ProcessAlive {
				allStopped = false
				break
			}
		}
		if allStopped || !time.Now().Before(deadline) {
			return
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (a *App) provablyOwnedTmuxSession(ctx context.Context, session, expectedItemID string, requireGeneratedName bool) (bool, error) {
	env, err := a.Tmux.SessionEnvironment(ctx, session)
	if err != nil {
		return false, err
	}
	itemID := strings.TrimSpace(env["WI_ID"])
	if !model.ValidID(itemID) || expectedItemID != "" && itemID != expectedItemID {
		return false, nil
	}
	if requireGeneratedName {
		shortID := itemID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		if !strings.HasPrefix(session, "wi-") || !strings.HasSuffix(session, "-"+shortID) {
			return false, nil
		}
	}
	itemDir := filepath.Clean(a.Store.ItemDir(itemID))
	if strings.TrimSpace(env["WI_DIR"]) == "" || filepath.Clean(env["WI_DIR"]) != itemDir {
		return false, nil
	}
	sessionDir := filepath.Join(itemDir, "sessions", "pi")
	if strings.TrimSpace(env["PI_CODING_AGENT_SESSION_DIR"]) == "" || filepath.Clean(env["PI_CODING_AGENT_SESSION_DIR"]) != sessionDir {
		return false, nil
	}
	return true, nil
}
