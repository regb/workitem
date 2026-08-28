package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	itemlock "github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/runtimepath"
)

type DeleteWorkItemsOptions struct {
	ResolveOptions
	Archived bool
}

type DeleteWorkItemsResult struct {
	DeletedIDs []string `json:"deleted_ids"`
	Count      int      `json:"count"`
	Warnings   []string `json:"warnings"`
}

// DeleteWorkItems irreversibly removes complete archived case files. It does
// not clean resources implicitly: archive/release/close/stop must happen first.
func (a *App) DeleteWorkItems(ctx context.Context, opts DeleteWorkItemsOptions) (DeleteWorkItemsResult, error) {
	items := []model.Manifest{}
	if opts.Archived {
		manifests, errs := a.Store.ListManifests()
		if len(errs) > 0 {
			return DeleteWorkItemsResult{}, fmt.Errorf("inspect archived work items: %w", errs[0])
		}
		for _, manifest := range manifests {
			if manifest.State == model.StateArchived {
				items = append(items, manifest)
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	} else {
		manifest, err := a.ResolveItem(ctx, opts.ResolveOptions)
		if err != nil {
			return DeleteWorkItemsResult{}, err
		}
		items = append(items, manifest)
	}
	// Stable per-item locks live outside item directories. Holding every lock
	// across reload, validation, and removal prevents restore/start/mutation from
	// racing a bulk deletion after its safety checks.
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	locks := make([]*itemlock.FileLock, 0, len(items))
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			_ = locks[i].Release()
		}
	}()
	for _, manifest := range items {
		locked, err := itemlock.Acquire(ctx, a.Store.LockPath(manifest.ID))
		if err != nil {
			return DeleteWorkItemsResult{}, fmt.Errorf("lock work item %s for deletion: %w", manifest.ID, err)
		}
		locks = append(locks, locked)
	}
	for index := range items {
		current, err := a.Store.LoadManifest(items[index].ID)
		if err != nil {
			return DeleteWorkItemsResult{}, err
		}
		items[index] = current
		if err := a.validateDeletableWorkItem(ctx, current); err != nil {
			return DeleteWorkItemsResult{}, err
		}
	}
	result := DeleteWorkItemsResult{DeletedIDs: []string{}, Warnings: []string{}}
	for _, manifest := range items {
		if err := a.Store.RemoveItem(manifest.ID); err != nil {
			return result, fmt.Errorf("delete work item %s: %w", manifest.ID, err)
		}
		operationalPaths := []string{}
		if root := strings.TrimSpace(a.AgentRuntimeStateRoot); root != "" {
			operationalPaths = append(operationalPaths, filepath.Join(root, "items", manifest.ID))
		}
		if root := strings.TrimSpace(a.AgentRuntimeSocketRoot); root != "" {
			operationalPaths = append(operationalPaths, filepath.Join(root, filepath.FromSlash(runtimepath.ControlItemDir(manifest.ID))))
		}
		for _, operationalPath := range operationalPaths {
			if err := os.RemoveAll(operationalPath); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("could not remove operational runtime files for %s: %v", manifest.ID, err))
			}
		}
		result.DeletedIDs = append(result.DeletedIDs, manifest.ID)
	}
	result.Count = len(result.DeletedIDs)
	return result, nil
}

func (a *App) validateDeletableWorkItem(ctx context.Context, manifest model.Manifest) error {
	if manifest.State != model.StateArchived {
		return fmt.Errorf("work item %s is %s; delete requires archived state", manifest.ID, manifest.State)
	}
	if runtime, err := a.Store.LoadAgentRuntime(manifest.ID); err != nil {
		return err
	} else if a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		return fmt.Errorf("agent runtime %s is still active; stop it before deleting work item %s", runtime.ID, manifest.ID)
	}
	if a.Tmux != nil && strings.TrimSpace(manifest.TerminalSessionName()) != "" {
		exists, err := a.Tmux.HasSession(ctx, manifest.TerminalSessionName())
		if err != nil {
			return fmt.Errorf("inspect terminal for work item %s: %w", manifest.ID, err)
		}
		if exists {
			return fmt.Errorf("terminal session %s still exists; close it before deleting work item %s", manifest.TerminalSessionName(), manifest.ID)
		}
	}
	if manifest.Checkout.Present() || manifest.Checkout.Path != nil {
		return fmt.Errorf("work item %s still has a materialized checkout; release it before deleting", manifest.ID)
	}
	return nil
}
