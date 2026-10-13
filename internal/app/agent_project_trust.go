package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	workspacecore "github.com/regb/workitem/internal/app/core/workspace"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

// Repository registration authorizes project-local agent resources, not direnv
// shell code. Approval belongs to this verified launch, never a parent path or
// a caller-controlled environment variable.
func (a *App) agentProjectTrusted(ctx context.Context, itemID, cwd string) (bool, error) {
	m, err := a.Store.LoadManifest(itemID)
	if err != nil {
		return false, err
	}
	claim := m.Checkout.Claim
	if !model.ValidID(m.Repository.ID) || m.Checkout.Specification.Kind != model.WorkspaceKindManagedSlot || claim == nil || a.DaemonSocketPath == "" {
		return false, nil
	}
	if claim.RepositoryKey != m.Repository.ID || !workspacecore.IsSlotPathName(claim.Slot) || filepath.Base(claim.Slot) != claim.Slot || claim.Executor != model.ExecutorLocal {
		return false, fmt.Errorf("checkout is not a local registered managed slot")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var source coordinator.RepositorySourceResult
	client := &coordinator.Client{SocketPath: a.DaemonSocketPath}
	if err := client.Call(requestCtx, coordinator.MethodRepositorySource, coordinator.RepositorySourceRequest{Selector: m.Repository.ID, MetadataOnly: true}, &source); err != nil {
		return false, err
	}
	if source.Repository.ID != m.Repository.ID {
		return false, fmt.Errorf("repository identity does not match checkout")
	}
	root, err := filepath.EvalSymlinks(filepath.Join(a.Store.WorktreesDir(), m.Repository.ID))
	if err != nil {
		return false, err
	}
	path, err := filepath.EvalSymlinks(claim.Path)
	if err != nil {
		return false, err
	}
	location, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative != claim.Slot || path != location {
		return false, fmt.Errorf("runtime directory is not its claimed managed slot")
	}
	info, err := os.Lstat(source.GitDir)
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("registered Git store is not a regular directory")
	}
	store, err := filepath.EvalSymlinks(source.GitDir)
	if err != nil {
		return false, err
	}
	if a.Git == nil {
		return false, fmt.Errorf("Git inspector is not configured")
	}
	detected, err := a.Git.DetectRepository(requestCtx, location, "")
	if err != nil {
		return false, err
	}
	gitRoot, err := filepath.EvalSymlinks(detected.Repository.RootAtCreation)
	if err != nil {
		return false, err
	}
	common, err := filepath.EvalSymlinks(detected.Repository.GitCommonDir)
	if err != nil {
		return false, err
	}
	if gitRoot != path || common != store {
		return false, fmt.Errorf("runtime directory is not a worktree of the registered Git store")
	}
	return true, nil
}
