package app

import (
	"context"
	"fmt"
	"strings"
)

// CommitWorkItemWorkspace retains checkout changes before a later release.
// It shares the resource-operation lock with all other workspace mutations.
func (a *App) CommitWorkItemWorkspace(ctx context.Context, opts ResolveOptions, message string) (string, []string, error) {
	m, operation, err := a.lockItemOperation(ctx, opts)
	if err != nil {
		return "", nil, err
	}
	defer operation.Release()
	if m.Checkout.Claim == nil {
		return "", nil, fmt.Errorf("item has no workspace claim")
	}
	runtime, err := a.AgentRuntimeStatus(ctx, ResolveOptions{Selector: m.ID})
	if err != nil {
		return "", nil, err
	}
	if runtime.Online {
		return "", nil, fmt.Errorf("agent is still running")
	}
	branch, err := a.Git.CurrentBranch(ctx, m.Checkout.Claim.Path)
	if err != nil {
		return "", nil, err
	}
	if branch != m.Checkout.Specification.Branch {
		return "", nil, fmt.Errorf("checkout branch does not match its claim")
	}
	removed, err := a.removeAttachedCopies(ctx, m)
	if err != nil {
		return "", removed, err
	}
	status, err := a.Git.StatusPorcelain(ctx, m.Checkout.Claim.Path)
	if err != nil || strings.TrimSpace(status) == "" {
		return "", removed, err
	}
	if message == "" {
		message = "wi: retain workspace changes"
	}
	commit, err := a.Git.CommitAll(ctx, m.Checkout.Claim.Path, message)
	return commit, removed, err
}
