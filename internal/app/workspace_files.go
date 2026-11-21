package app

import (
	"context"
	"errors"
	"github.com/regb/workitem/internal/apperr"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/workspacefiles"
)

var (
	// ErrNoWorkspaceClaim means the item has no checkout to read or edit.
	ErrNoWorkspaceClaim = errors.New("item has no workspace claim")
	// ErrWorkspaceClaimChanged means the checkout was released or reclaimed
	// since the caller read from it.
	ErrWorkspaceClaimChanged = apperr.Stalef("workspace claim changed since it was read; reload before continuing")
	// ErrCheckoutBranch means the checkout is not on the item's branch, so
	// Wi does not edit it.
	ErrCheckoutBranch = errors.New("checkout branch does not match its claim")
	// ErrNotActive refuses an edit to an item that is not active.
	ErrNotActive = errors.New("item is not active")
)

// WorkspaceClaim is the fence a caller passes back to read or save a file.
type WorkspaceClaim struct {
	ID         string
	Generation uint32
}

// lockClaim takes the item's operation lock and checks that its current claim
// is the one the caller read from. The caller releases the lock.
func (a *App) lockClaim(ctx context.Context, opts ResolveOptions, expected WorkspaceClaim) (model.Manifest, func(), error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return model.Manifest{}, nil, err
	}
	release := func() { _ = operation.Release() }
	claim := m.Checkout.Claim
	if claim == nil {
		release()
		return m, nil, ErrNoWorkspaceClaim
	}
	if claim.ID != expected.ID || claim.Generation != uint64(expected.Generation) {
		release()
		return m, nil, ErrWorkspaceClaimChanged
	}
	return m, release, nil
}

// WorkspaceFilePaths lists the checkout's tracked and non-ignored untracked
// paths with the claim they were listed from.
func (a *App) WorkspaceFilePaths(ctx context.Context, opts ResolveOptions) ([]string, bool, WorkspaceClaim, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return nil, false, WorkspaceClaim{}, err
	}
	defer operation.Release()
	claim := m.Checkout.Claim
	if claim == nil {
		return nil, false, WorkspaceClaim{}, ErrNoWorkspaceClaim
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	listed, err := a.Git.ListFiles(ctx, claim.Path)
	if err != nil {
		return nil, false, WorkspaceClaim{}, err
	}
	paths, truncated := workspacefiles.Paths(listed)
	return paths, truncated, WorkspaceClaim{ID: claim.ID, Generation: uint32(claim.Generation)}, nil
}

// WorkspaceFile reads one file of the expected claim.
func (a *App) WorkspaceFile(ctx context.Context, opts ResolveOptions, claim WorkspaceClaim, p string) (workspacefiles.File, error) {
	if err := workspacefiles.ValidPath(p); err != nil {
		return workspacefiles.File{}, err
	}
	m, release, err := a.lockClaim(ctx, opts, claim)
	if err != nil {
		return workspacefiles.File{}, err
	}
	defer release()
	return workspacefiles.Read(m.Checkout.Claim.Path, p)
}

// WorkspaceDocument reads one PDF of the expected claim.
func (a *App) WorkspaceDocument(ctx context.Context, opts ResolveOptions, claim WorkspaceClaim, p string) ([]byte, string, error) {
	if err := workspacefiles.ValidPath(p); err != nil {
		return nil, "", err
	}
	m, release, err := a.lockClaim(ctx, opts, claim)
	if err != nil {
		return nil, "", err
	}
	defer release()
	return workspacefiles.ReadDocument(m.Checkout.Claim.Path, p)
}

// SaveWorkspaceFile replaces an existing text file of an active item's
// expected claim, when its content still has the expected digest and the
// checkout is on the item's branch.
func (a *App) SaveWorkspaceFile(ctx context.Context, opts ResolveOptions, claim WorkspaceClaim, p, content, digest string) (workspacefiles.File, error) {
	if err := workspacefiles.ValidPath(p); err != nil {
		return workspacefiles.File{}, err
	}
	m, release, err := a.lockClaim(ctx, opts, claim)
	if err != nil {
		return workspacefiles.File{}, err
	}
	defer release()
	if m.State != model.StateActive {
		return workspacefiles.File{}, ErrNotActive
	}
	branch, err := a.Git.CurrentBranch(ctx, m.Checkout.Claim.Path)
	if err != nil {
		return workspacefiles.File{}, err
	}
	if branch != expectedCheckoutBranch(m) {
		return workspacefiles.File{}, ErrCheckoutBranch
	}
	return workspacefiles.Save(m.Checkout.Claim.Path, p, content, digest)
}
