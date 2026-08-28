package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/app"
	agentcore "github.com/regb/workitem/internal/app/core/primaryagent"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func coordinatorAgentStatus(ctx context.Context, cfg Config, selector string, staleAfter time.Duration) (app.AgentStatusResult, error) {
	manifest, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return app.AgentStatusResult{}, err
	}
	projection, err := cfg.Coordinator.AgentObservation(ctx, manifest.ID)
	if err != nil {
		return app.AgentStatusResult{}, err
	}
	if !projection.Found {
		if _, barrierErr := cfg.Coordinator.ActivityBarrier(ctx); barrierErr != nil {
			return app.AgentStatusResult{}, barrierErr
		}
		projection, err = cfg.Coordinator.AgentObservation(ctx, manifest.ID)
		if err != nil {
			return app.AgentStatusResult{}, err
		}
	}
	if !projection.Found {
		return app.AgentStatusResult{}, fmt.Errorf("daemon has not built agent observation for %s", manifest.ID)
	}
	value := projection.Observation
	piStatus, piWarnings := coordinatorPiSessionObserver(cfg.Coordinator)(manifest, time.Now())
	process := app.AgentProcessStatus{RuntimeID: value.Process.RuntimeID, RuntimeMode: value.Process.RuntimeMode, RuntimeHostPID: value.Process.RuntimeHostPID, TmuxSession: value.Process.TmuxSession, TmuxWindow: value.Process.TmuxWindow, TmuxPaneID: value.Process.TmuxPaneID, TmuxPaneIndex: value.Process.TmuxPaneIndex, TmuxPanePID: value.Process.TmuxPanePID, TmuxPaneCommand: value.Process.TmuxPaneCommand, TmuxPanePath: value.Process.TmuxPanePath, TmuxPaneAlive: value.Process.TmuxPanePID > 0, PiPID: value.Process.PiPID, PiProcessAlive: value.ProcessOnline, Online: value.ProcessOnline, DiscoverySource: value.Process.DiscoverySource}
	var worktree *app.WorktreeStatus
	if value.Worktree != nil {
		worktree = &app.WorktreeStatus{Status: value.Worktree.Status, Reason: value.Worktree.Reason, CheckoutPath: value.Worktree.CheckoutPath, Head: value.Worktree.Head, ExpectedBranch: value.Worktree.ExpectedBranch, CurrentBranch: value.Worktree.CurrentBranch, BranchMatches: value.Worktree.BranchMatches, BranchMismatch: value.Worktree.BranchMismatch, Dirty: value.Worktree.Dirty, HasChanges: value.Worktree.HasChanges}
	}
	status, reason := value.Status, value.Reason
	if status == "busy" && staleAfter > 0 && value.LastActivityAt != nil && time.Since(*value.LastActivityAt) > staleAfter {
		status = "problem"
		reason = "latest Pi session activity is incomplete and older than " + staleAfter.String()
	}
	warnings := append(append([]string(nil), value.Warnings...), piWarnings...)
	if !projection.Projection.Fresh {
		warnings = append(warnings, "daemon agent observation is stale")
	}
	return app.AgentStatusResult{WorkItemID: manifest.ID, Status: status, Reason: reason, ObservedAt: value.ObservedAt, Terminal: value.Terminal, Runtime: value.Runtime, Process: process, PiSession: piStatus, Worktree: worktree, Manifest: manifest, Warnings: warnings}, nil
}

func coordinatorWorkListObserver(client *coordinator.Client) viewapp.Observe {
	return func(ctx context.Context, itemID string) (viewapp.Observation, error) {
		queryCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		projection, err := client.AgentObservation(queryCtx, itemID)
		if err != nil {
			return viewapp.Observation{}, err
		}
		if !projection.Found {
			return viewapp.Observation{}, fmt.Errorf("daemon has not built agent observation for %s", itemID)
		}
		observation := coordinatorViewObservation(projection.Observation)
		if !projection.Projection.Fresh {
			observation.Warnings = append(observation.Warnings, "daemon agent observation is stale")
		}
		return observation, nil
	}
}

func coordinatorViewObservation(value coordinator.AgentObservation) viewapp.Observation {
	age := int64(0)
	if value.LastActivityAt != nil {
		age = int64(time.Since(*value.LastActivityAt).Seconds())
	}
	var worktree *viewapp.WorktreeStatus
	if value.Worktree != nil {
		worktree = &viewapp.WorktreeStatus{Status: value.Worktree.Status, Reason: value.Worktree.Reason, CheckoutPath: value.Worktree.CheckoutPath, Head: value.Worktree.Head, ExpectedBranch: value.Worktree.ExpectedBranch, CurrentBranch: value.Worktree.CurrentBranch, BranchMatches: value.Worktree.BranchMatches, BranchMismatch: value.Worktree.BranchMismatch, Dirty: value.Worktree.Dirty, HasChanges: value.Worktree.HasChanges}
	}
	return viewapp.Observation{Status: value.Status, Reason: value.Reason, ProcessOnline: value.ProcessOnline, TurnState: value.TurnState, LastActivityAgeSeconds: age, Worktree: worktree, Activity: viewapp.Activity{LastRequestedAt: value.Activity.LastRequestedAt, LastCompletedAt: value.Activity.LastCompletedAt, LastDeferredAt: value.Activity.LastDeferredAt}, Warnings: append([]string(nil), value.Warnings...)}
}

func coordinatorPiSessionObserver(client *coordinator.Client) agentcore.PiSessionObserver {
	return func(manifest model.Manifest, now time.Time) (agentcore.PiSessionStatus, []string) {
		if manifest.RootPiSession == nil {
			return agentcore.PiSessionStatus{UnavailableReason: "no root Pi session is recorded for this work item"}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		projection, err := client.PiSession(ctx, manifest.ID)
		if err != nil {
			return agentcore.PiSessionStatus{ID: manifest.RootPiSession.ID, PathRel: manifest.RootPiSession.Path, Source: "daemon.pi_session", UnavailableReason: "could not read daemon Pi session projection: " + err.Error()}, nil
		}
		if !projection.Found {
			return agentcore.PiSessionStatus{ID: manifest.RootPiSession.ID, PathRel: manifest.RootPiSession.Path, Source: "daemon.pi_session", UnavailableReason: "daemon has not indexed the Pi session yet"}, projection.Projection.Warnings
		}
		index := projection.Session
		status := agentcore.PiSessionStatus{
			ID:                    index.SessionID,
			PathRel:               manifest.RootPiSession.Path,
			Source:                "daemon.pi_session",
			Exists:                true,
			SizeBytes:             index.Size,
			EntriesScanned:        boundedInt(index.EntriesScanned),
			MalformedLines:        boundedInt(index.MalformedLines),
			InferredTurnState:     index.InferredTurnState,
			LastEvent:             coordinatorPiFact(index.LastEvent),
			LastTurnActivity:      coordinatorPiFact(index.LastTurnActivity),
			LastUserPrompt:        coordinatorPiFact(index.LastUserPrompt),
			LastAssistantMessage:  coordinatorPiFact(index.LastAssistantMessage),
			LastTerminalAssistant: coordinatorPiFact(index.LastTerminalAssistant),
			LastToolActivity:      coordinatorPiFact(index.LastToolActivity),
		}
		if status.LastTurnActivity != nil && !status.LastTurnActivity.Timestamp.IsZero() {
			status.LastActivityAgeSeconds = int64(now.Sub(status.LastTurnActivity.Timestamp).Seconds())
		}
		return status, projection.Projection.Warnings
	}
}

func coordinatorPiFact(fact *coordinator.PiEventFact) *agentcore.PiSessionEventSummary {
	if fact == nil {
		return nil
	}
	return &agentcore.PiSessionEventSummary{Line: boundedInt(fact.Line), Type: fact.Type, Timestamp: fact.Timestamp, Role: fact.Role, StopReason: fact.StopReason, ContentTypes: append([]string(nil), fact.ContentTypes...), Terminal: fact.Terminal, Failed: fact.Failed}
}

func boundedInt(value uint64) int {
	maximum := uint64(^uint(0) >> 1)
	if value > maximum {
		return int(maximum)
	}
	return int(value)
}
