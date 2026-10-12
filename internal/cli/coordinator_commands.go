package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/regb/workitem/internal/app"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func createCoordinatorWorkItem(ctx context.Context, cfg Config, opts app.NewWorkItemOptions) (app.NewWorkItemResult, error) {
	prepared, err := cfg.App.PrepareNewWorkItem(ctx, opts)
	if err != nil {
		return app.NewWorkItemResult{}, err
	}
	commandID, err := model.NewID()
	if err != nil {
		return app.NewWorkItemResult{}, err
	}
	command := coordinator.CreateItemCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Manifest: prepared.Manifest, DescriptionDigest: coordinator.DescriptionDigest(prepared.Description), Actor: "user", CreatedAt: prepared.Manifest.CreatedAt}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	created, err := cfg.Coordinator.CreateItem(requestCtx, coordinator.CreateItemRequest{Command: command, Description: prepared.Description})
	cancel()
	if err != nil {
		return app.NewWorkItemResult{}, err
	}
	return app.NewWorkItemResult{Manifest: created.Manifest, ItemDir: prepared.ItemDir, Warnings: prepared.Warnings}, nil
}

type coordinatorManifestCommandOptions struct {
	commandType string
	labels      []string
	deepWork    *bool
	targetState string
	eventType   string
	force       bool
	maxActive   int
}

func executeCoordinatorManifestCommand(ctx context.Context, cfg Config, selector string, options coordinatorManifestCommandOptions) (coordinator.CanonicalManifest, coordinator.ManifestCommandResult, error) {
	manifest, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return coordinator.CanonicalManifest{}, coordinator.ManifestCommandResult{}, err
	}
	commandID, err := model.NewID()
	if err != nil {
		return coordinator.CanonicalManifest{}, coordinator.ManifestCommandResult{}, err
	}
	for attempt := 0; attempt < 8; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		canonical, err := cfg.Coordinator.CanonicalManifest(requestCtx, manifest.ID)
		cancel()
		if err != nil {
			return coordinator.CanonicalManifest{}, coordinator.ManifestCommandResult{}, err
		}
		command := coordinator.ManifestCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Type: options.commandType, ItemID: manifest.ID, ExpectedRevision: &canonical.Revision, Actor: "user", Labels: append([]string(nil), options.labels...), DeepWork: options.deepWork, TargetState: options.targetState, EventType: options.eventType, Force: options.force, MaxActive: options.maxActive, CreatedAt: time.Now().UTC()}
		requestCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		result, err := cfg.Coordinator.ExecuteManifestCommand(requestCtx, command)
		cancel()
		if err == nil {
			return canonical, result, nil
		}
		if attempt < 7 && stringsContainsRevisionConflict(err.Error()) {
			continue
		}
		return canonical, coordinator.ManifestCommandResult{}, err
	}
	return coordinator.CanonicalManifest{}, coordinator.ManifestCommandResult{}, fmt.Errorf("manifest command retry exhausted")
}

func coordinatorStateTransition(ctx context.Context, cfg Config, selector, target, eventType string, force bool) (app.StateTransitionResult, error) {
	capacity := cfg.App.DeepWorkCapacity()
	before, commandResult, err := executeCoordinatorManifestCommand(ctx, cfg, selector, coordinatorManifestCommandOptions{commandType: coordinator.CommandStateSet, targetState: target, eventType: eventType, force: force, maxActive: cfg.App.DeepWorkConfig.MaxActive})
	if err != nil {
		return app.StateTransitionResult{}, err
	}
	result := app.StateTransitionResult{WorkItemID: commandResult.Manifest.ID, PreviousState: before.Manifest.State, State: commandResult.Manifest.State, Changed: commandResult.Changed, Manifest: commandResult.Manifest, DeepWork: commandResult.Manifest.DeepWork, Warnings: []string{}}
	if before.Manifest.State != model.StateArchived && target != model.StateArchived {
		result.Capacity = &capacity
	}
	return result, nil
}

func coordinatorWaitAndQueue(ctx context.Context, cfg Config, selector string, labelRules map[string]bool) (app.StateTransitionResult, app.AttentionQueueResult, *app.NextQueueSelection, error) {
	barrierCtx, barrierCancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	barrier, err := cfg.Coordinator.ActivityBarrier(barrierCtx)
	barrierCancel()
	if err != nil {
		return app.StateTransitionResult{}, app.AttentionQueueResult{}, nil, fmt.Errorf("refresh attention before waiting: %w", err)
	}
	manifest, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return app.StateTransitionResult{}, app.AttentionQueueResult{}, nil, err
	}
	capacity := cfg.App.DeepWorkCapacity()
	commandID, err := model.NewID()
	if err != nil {
		return app.StateTransitionResult{}, app.AttentionQueueResult{}, nil, err
	}
	createdAt := time.Now().UTC()
	for attempt := 0; attempt < 8; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		canonical, err := cfg.Coordinator.CanonicalManifest(requestCtx, manifest.ID)
		cancel()
		if err != nil {
			return app.StateTransitionResult{}, app.AttentionQueueResult{}, nil, err
		}
		command := coordinator.ManifestCommand{ID: commandID, ProtocolVersion: coordinator.ProtocolVersion, Type: coordinator.CommandStateSet, ItemID: manifest.ID, ExpectedRevision: &canonical.Revision, Actor: "user", TargetState: model.StateWaiting, EventType: "work_item.state_set", MaxActive: cfg.App.DeepWorkConfig.MaxActive, CreatedAt: createdAt}
		request := coordinator.ManifestCommandQueueRequest{Command: command, Queue: coordinator.ActionabilityQueueOptions{Strategy: cfg.App.AttentionConfig.Priority, LabelRules: labelRules}}
		requestCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
		result, err := cfg.Coordinator.ExecuteManifestCommandWithQueue(requestCtx, request)
		cancel()
		if err != nil {
			if attempt < 7 && stringsContainsRevisionConflict(err.Error()) {
				continue
			}
			return app.StateTransitionResult{}, app.AttentionQueueResult{}, nil, err
		}
		transition := app.StateTransitionResult{WorkItemID: result.Command.Manifest.ID, PreviousState: canonical.Manifest.State, State: result.Command.Manifest.State, Changed: result.Command.Changed, Manifest: result.Command.Manifest, DeepWork: result.Command.Manifest.DeepWork, Capacity: &capacity, Warnings: []string{}}
		queue := coordinatorProjectedQueue(cfg, result.Queue, barrier.Projection.Warnings)
		return transition, queue, coordinatorNextSelection(result.Selection), nil
	}
	return app.StateTransitionResult{}, app.AttentionQueueResult{}, nil, fmt.Errorf("wait-and-queue command retry exhausted")
}

func coordinatorActionabilitySnapshot(ctx context.Context, cfg Config, labelRules map[string]bool) (app.AttentionQueueResult, *app.NextQueueSelection, error) {
	currentItemID := ""
	if current, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{CWD: cfg.CWD, Env: cfg.Env}); err == nil {
		currentItemID = current.ID
	}
	requestCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	result, err := cfg.Coordinator.ActionabilitySnapshot(requestCtx, coordinator.ActionabilitySnapshotRequest{CurrentItemID: currentItemID, Queue: coordinator.ActionabilityQueueOptions{Strategy: cfg.App.AttentionConfig.Priority, LabelRules: labelRules}})
	cancel()
	if err != nil {
		return app.AttentionQueueResult{}, nil, err
	}
	return coordinatorProjectedQueue(cfg, result.Queue, result.Projection.Warnings), coordinatorNextSelection(result.Selection), nil
}

func coordinatorNextSelection(selection coordinator.ActionabilitySelection) *app.NextQueueSelection {
	if !selection.Found {
		return nil
	}
	return &app.NextQueueSelection{Index: selection.Index, WorkItemID: selection.WorkItemID, CurrentInQueue: selection.CurrentInQueue, Wrapped: selection.Wrapped}
}

func coordinatorProjectedQueue(cfg Config, projected coordinator.ActionabilityQueueResult, warnings []string) app.AttentionQueueResult {
	queue := app.AttentionQueueResult{Strategy: projected.Strategy, Candidates: make([]app.AttentionCandidate, 0, len(projected.Candidates)), Warnings: append([]string(nil), warnings...)}
	for _, candidate := range projected.Candidates {
		item := cfg.App.ProjectWorkListItem(candidate.Manifest)
		activity := viewapp.Activity{LastRequestedAt: candidate.Activity.LastRequestedAt, LastCompletedAt: candidate.Activity.LastCompletedAt, LastDeferredAt: candidate.Activity.LastDeferredAt}
		item.Attention = &activity
		item.AttentionRank = candidate.Rank
		queue.Candidates = append(queue.Candidates, app.AttentionCandidate{Item: item, Activity: activity, Rank: candidate.Rank})
	}
	return queue
}

func hydrateCoordinatorSelectedItem(ctx context.Context, cfg Config, item *app.WorkListItem) []string {
	if cfg.Coordinator == nil || item == nil || item.ID == "" {
		return nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	result, err := cfg.Coordinator.AgentObservation(requestCtx, item.ID)
	cancel()
	if err != nil {
		return []string{"could not hydrate selected agent observation: " + err.Error()}
	}
	if !result.Found {
		return []string{"daemon has not built selected agent observation"}
	}
	observation := coordinatorViewObservation(result.Observation)
	markers := viewapp.Markers{Busy: cfg.App.AgentStatusConfig.Markers.Busy, Idle: cfg.App.AgentStatusConfig.Markers.Idle, Problem: cfg.App.AgentStatusConfig.Markers.Problem}
	agent := viewapp.ProjectAgent(observation, markers)
	item.Agent = &agent
	item.Worktree = observation.Worktree
	return observation.Warnings
}

func stringsContainsRevisionConflict(message string) bool {
	return strings.Contains(message, "revision conflict")
}
