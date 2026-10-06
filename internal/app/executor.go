package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	executorprotocol "github.com/regb/workitem/executor/protocol"
	executorclient "github.com/regb/workitem/internal/executor"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/version"
)

type ExecutorClient interface {
	Doctor(context.Context) (executorprotocol.DoctorResult, error)
	DeleteItem(context.Context, string) error
	ReconcileItems(context.Context, []string) (executorprotocol.ItemsResult, error)
	EnsureWorkspace(context.Context, model.Manifest, string, string, string, string) (executorprotocol.WorkspaceResult, error)
	WorkspaceStatus(context.Context, string) (executorprotocol.WorkspaceResult, error)
	ReleaseWorkspace(context.Context, model.Manifest, bool) (executorprotocol.WorkspaceResult, error)
	ImportWorkspaceBundle(context.Context, model.Manifest, []byte) error
	ExecuteWorkspaceTool(context.Context, executorprotocol.ToolRequest) (executorprotocol.ToolResult, error)
	Shutdown(context.Context) error
}

type ExecutorInfo struct {
	Name        string `json:"name"`
	Destination string `json:"destination"`
}

type ExecutorSelectionResult struct {
	WorkItemID string         `json:"work_item_id"`
	Previous   string         `json:"previous"`
	Executor   string         `json:"executor"`
	Changed    bool           `json:"changed"`
	Manifest   model.Manifest `json:"manifest"`
}

type ExecutorDoctorResult struct {
	Executor     ExecutorInfo                  `json:"executor"`
	LocalVersion string                        `json:"local_version"`
	Compatible   bool                          `json:"compatible"`
	Worker       executorprotocol.DoctorResult `json:"worker"`
	Warnings     []string                      `json:"warnings"`
}

func (a *App) SelectExecutor(ctx context.Context, opts ResolveOptions, name string) (ExecutorSelectionResult, error) {
	manifest, err := a.ResolveItem(ctx, opts)
	if err != nil {
		return ExecutorSelectionResult{}, err
	}
	name, err = a.resolveExecutorName(name, false)
	if err != nil {
		return ExecutorSelectionResult{}, err
	}
	result := ExecutorSelectionResult{WorkItemID: manifest.ID, Previous: manifest.Checkout.Specification.Executor, Executor: name, Manifest: manifest}
	if manifest.Checkout.Specification.Kind != model.WorkspaceKindManagedSlot {
		return result, fmt.Errorf("repository-home checkout cannot select an executor")
	}
	previous := manifest.Checkout.Specification.Executor
	if previous == "" {
		previous = model.ExecutorLocal
	}
	if previous == name {
		result.Previous, result.Executor, result.Manifest = previous, name, manifest
		return result, nil
	}
	materialized, err := a.workspaceEverMaterialized(manifest)
	if err != nil {
		return result, err
	}
	if materialized {
		return result, fmt.Errorf("executor selection is fixed after the first workspace materialization; workspace movement is not supported yet")
	}
	if manifest.Checkout.Present() {
		return result, fmt.Errorf("workspace has a claim before its first recorded materialization")
	}
	if manifest.PrimaryConversation != nil {
		return result, fmt.Errorf("work item has a conversation before its first recorded materialization")
	}
	if runtime, loadErr := a.Store.LoadAgentRuntime(manifest.ID); loadErr != nil {
		return result, loadErr
	} else if runtime != nil {
		return result, fmt.Errorf("work item has a runtime before its first recorded materialization")
	}
	manifest.Checkout.Specification.Executor = name
	manifest.UpdatedAt = a.now()
	if err := a.Store.SaveManifest(ctx, manifest); err != nil {
		return ExecutorSelectionResult{}, err
	}
	_ = a.Store.AppendEvent(ctx, manifest.ID, model.NewEvent(a.now(), "checkout.executor_selected", "user", map[string]any{"previous_executor": previous, "executor": name}))
	result.Previous, result.Executor, result.Changed, result.Manifest = previous, name, true, manifest
	return result, nil
}

func (a *App) workspaceEverMaterialized(manifest model.Manifest) (bool, error) {
	if manifest.Checkout.EverMaterialized() {
		return true, nil
	}
	events, err := a.Store.ReadEvents(manifest.ID)
	if err != nil {
		return false, fmt.Errorf("read workspace history: %w", err)
	}
	for _, event := range events {
		if event.Type == "checkout.assigned" {
			return true, nil
		}
	}
	return false, nil
}

func (a *App) Executors() []ExecutorInfo {
	result := make([]ExecutorInfo, 0, len(a.ExecutorsConfig))
	for name, definition := range a.ExecutorsConfig {
		result = append(result, ExecutorInfo{Name: name, Destination: definition.Destination})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (a *App) executorClient(name string) (ExecutorClient, error) {
	definition, ok := a.ExecutorsConfig[name]
	if !ok {
		return nil, fmt.Errorf("executor %q is not configured", name)
	}
	if client := a.ExecutorClients[name]; client != nil {
		return client, nil
	}
	return executorclient.Client{Name: name, Definition: definition, MasterID: a.MasterID}, nil
}

func (a *App) ensureRemoteWorkspace(ctx context.Context, manifest model.Manifest) (model.Manifest, []string, error) {
	executorName := manifest.Checkout.Specification.Executor
	client, err := a.executorClient(executorName)
	if err != nil {
		return model.Manifest{}, nil, err
	}
	if manifest.Checkout.Present() {
		if _, err := client.WorkspaceStatus(ctx, manifest.ID); err != nil {
			return model.Manifest{}, nil, fmt.Errorf("inspect existing executor workspace claim %s: %w", manifest.Checkout.Claim.ID, err)
		}
		return manifest, nil, nil
	}
	doctor, err := client.Doctor(ctx)
	if err != nil {
		return model.Manifest{}, nil, err
	}
	if doctor.Version != version.Current().Version {
		return model.Manifest{}, nil, fmt.Errorf("executor %s wi version %s does not match coordinator version %s", executorName, doctor.Version, version.Current().Version)
	}
	requiredTools := []string{"git"}
	for _, tool := range requiredTools {
		if doctor.Tools[tool] == "missing" {
			return model.Manifest{}, nil, fmt.Errorf("executor %s is missing required tool %s", executorName, tool)
		}
	}
	warnings := []string{}
	if reconciled, reconcileErr := client.ReconcileItems(ctx, a.executorReplicaKeepIDs(executorName)); reconcileErr != nil {
		warnings = append(warnings, "could not reconcile executor item replicas: "+reconcileErr.Error())
	} else if len(reconciled.BlockedIDs) > 0 {
		warnings = append(warnings, fmt.Sprintf("executor retained %d stale item replicas with live resources", len(reconciled.BlockedIDs)))
	}
	repositoryKey := executorRepositoryKey(manifest.Repository)
	existingSlot := ""
	if manifest.Checkout.Claim != nil {
		existingSlot = manifest.Checkout.Claim.Slot
	}
	slot, err := a.remoteSlot(manifest.ID, executorName, repositoryKey, existingSlot)
	if err != nil {
		return model.Manifest{}, nil, err
	}
	description, err := os.ReadFile(filepath.Join(a.Store.ItemDir(manifest.ID), model.DescriptionFilename))
	if err != nil && !os.IsNotExist(err) {
		return model.Manifest{}, nil, err
	}
	result, err := client.EnsureWorkspace(ctx, manifest, string(description), repositoryKey, slot, "")
	if err != nil {
		return model.Manifest{}, nil, err
	}
	if strings.TrimSpace(result.ClaimID) == "" || result.Executor != executorName || result.RepositoryKey != repositoryKey {
		return model.Manifest{}, nil, fmt.Errorf("executor returned an invalid workspace claim")
	}
	claim := model.WorkspaceClaim{ID: result.ClaimID, Generation: result.ClaimGeneration, Executor: executorName, RepositoryKey: repositoryKey, Slot: result.Slot, Path: result.Path}
	if err := manifest.Checkout.AcceptClaim(claim); err != nil {
		return model.Manifest{}, nil, fmt.Errorf("accept executor workspace claim: %w", err)
	}
	manifest.UpdatedAt = a.now()
	if err := a.Store.SaveManifest(ctx, manifest); err != nil {
		return model.Manifest{}, nil, err
	}
	_ = a.Store.AppendEvent(ctx, manifest.ID, model.NewEvent(a.now(), "checkout.assigned", "wi", map[string]any{"claim_id": result.ClaimID, "claim_generation": result.ClaimGeneration, "executor": executorName, "slot": result.Slot, "path": result.Path, "source": result.Source}))
	return manifest, warnings, nil
}

func (a *App) executorReplicaKeepIDs(executorName string) []string {
	items, _ := a.Store.ListManifests()
	ids := []string{}
	for _, item := range items {
		selected := item.Checkout.Specification.Executor == executorName
		conversation := item.PrimaryConversation != nil && item.PrimaryConversation.Executor == executorName
		if selected || conversation {
			ids = append(ids, item.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (a *App) remoteSlot(itemID, executorName, repositoryKey, existing string) (string, error) {
	if strings.TrimSpace(existing) != "" {
		return existing, nil
	}
	items, errs := a.Store.ListManifests()
	if len(errs) > 0 {
		return "", errs[0]
	}
	used := map[int]bool{}
	for _, item := range items {
		if item.ID == itemID || item.Checkout.Specification.Executor != executorName || executorRepositoryKey(item.Repository) != repositoryKey {
			continue
		}
		if item.Checkout.Claim != nil {
			if n, err := strconv.Atoi(strings.TrimPrefix(item.Checkout.Claim.Slot, "slot-")); err == nil && n > 0 {
				used[n] = true
			}
		}
	}
	for index := 1; ; index++ {
		if !used[index] {
			return fmt.Sprintf("slot-%04d", index), nil
		}
	}
}

func executorRepositoryKey(repository model.Repository) string {
	return model.RepositoryKey(repository)
}

func (a *App) ExecutorShutdown(ctx context.Context, name string) error {
	client, err := a.executorClient(strings.TrimSpace(name))
	if err != nil {
		return err
	}
	return client.Shutdown(ctx)
}

func (a *App) ExecutorDoctor(ctx context.Context, name string) (ExecutorDoctorResult, error) {
	name = strings.TrimSpace(name)
	definition, ok := a.ExecutorsConfig[name]
	if !ok {
		return ExecutorDoctorResult{}, fmt.Errorf("executor %q is not configured", name)
	}
	client, err := a.executorClient(name)
	if err != nil {
		return ExecutorDoctorResult{}, err
	}
	worker, err := client.Doctor(ctx)
	if err != nil {
		return ExecutorDoctorResult{}, fmt.Errorf("inspect executor %s: %w", name, err)
	}
	localVersion := version.Current().Version
	result := ExecutorDoctorResult{Executor: ExecutorInfo{Name: name, Destination: definition.Destination}, LocalVersion: localVersion, Compatible: worker.Version == localVersion, Worker: worker, Warnings: []string{}}
	if !result.Compatible {
		result.Warnings = append(result.Warnings, fmt.Sprintf("worker wi version %s does not match coordinator version %s", worker.Version, localVersion))
	}
	for tool, value := range worker.Tools {
		if value == "missing" {
			result.Warnings = append(result.Warnings, "worker tool "+tool+" is missing")
		}
	}
	sort.Strings(result.Warnings)
	return result, nil
}
