package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/regb/workitem/internal/executor"
	"github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

// mergeExecutor rebases in the executor and integrates only the verified returned commit.
// The item's checkout policy and conversation never move to the host.
func (a *App) mergeExecutor(ctx context.Context, m model.Manifest, opts MergeOptions) (result MergeResult, err error) {
	guard, e := lock.Acquire(ctx, a.Store.LockPath(m.ID)+".executor")
	if e != nil {
		return result, e
	}
	defer guard.Release()
	m, err = a.Store.LoadManifest(m.ID)
	if err != nil {
		return result, err
	}
	result = MergeResult{WorkItemID: m.ID, SourceBranch: m.Checkout.Specification.Branch, SourcePath: m.Checkout.Claim.Path, Warnings: []string{}}
	if m.Checkout.Claim == nil || m.Checkout.Claim.Executor == nil || !m.Checkout.Claim.Executor.Ready {
		return result, fmt.Errorf("executor is not materialized")
	}
	if runtime, e := a.Store.LoadAgentRuntime(m.ID); e != nil {
		return result, e
	} else if a.primaryAgentService().ObserveOwnership(runtime).ProcessAlive {
		return result, fmt.Errorf("stop the agent runtime before merging its executor")
	}
	c, err := a.executorClient(m)
	if err != nil {
		return result, err
	}
	if err := c.StopJobs(ctx); err != nil {
		return result, err
	}
	if err := a.ensureCheckoutReleasable(ctx, m); err != nil {
		return result, err
	}
	mg, ok := a.Git.(mergeGit)
	if !ok {
		return result, fmt.Errorf("Git adapter does not support merge")
	}
	root := m.Repository.OperationalRoot()
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		target, err = mg.DefaultBranch(ctx, root)
		if err != nil {
			return result, err
		}
	}
	if target == result.SourceBranch {
		return result, fmt.Errorf("source and target branches must differ")
	}
	result.TargetBranch = target
	result.TargetOldSHA, err = mg.ResolveBranchSHA(ctx, root, target)
	if err != nil {
		return result, err
	}
	m, err = a.exportExecutor(ctx, m)
	if err != nil {
		return result, err
	}
	result.SourceOldSHA = m.Checkout.Claim.Executor.ExportedHead
	targetTree, err := mg.TargetWorktreeForBranch(ctx, root, target)
	if err != nil {
		return result, err
	}
	if targetTree != nil {
		result.TargetWorktreePath = targetTree.Path
	}
	bundle, cleanup, err := executor.Bundle(ctx, root, result.SourceBranch, result.SourceOldSHA, target)
	if err != nil {
		return result, err
	}
	defer cleanup()
	file, err := os.Open(bundle)
	if err != nil {
		return result, err
	}
	err = c.Update(ctx, file)
	_ = file.Close()
	if err != nil {
		return result, err
	}
	a.appendMergeEvent(ctx, m.ID, "merge.started", mergeEventData(result, "", ""), &result)
	if err = c.Rebase(ctx, result.TargetOldSHA); err != nil {
		return result, fmt.Errorf("executor rebase failed; resolve in the executor shell and retry: %w", err)
	}
	result.Rebased = true
	// Once rebased, failures attempt conditional rollback, never overwrite work
	// made concurrently from the interactive shell.
	defer func() {
		if err == nil {
			return
		}
		if result.TargetAdvanced && !result.TargetSynced {
			if _, e := executor.Git(ctx, root, "update-ref", "refs/heads/"+target, result.TargetOldSHA, result.SourceNewSHA); e != nil {
				result.Warnings = append(result.Warnings, "target rollback failed: "+e.Error())
			} else {
				result.TargetAdvanced = false
			}
		}
		if !result.TargetAdvanced && result.SourceNewSHA != "" {
			if e := c.Reset(ctx, result.SourceNewSHA, result.SourceOldSHA); e != nil {
				result.Warnings = append(result.Warnings, "executor rollback failed: "+e.Error())
			} else {
				result.RolledBack = true
				if _, e := a.exportExecutor(ctx, m); e != nil {
					result.Warnings = append(result.Warnings, "could not export rolled-back HEAD: "+e.Error())
				}
			}
		}
		a.appendMergeEvent(ctx, m.ID, "merge.failed", mergeEventData(result, "executor_integration", err.Error()), &result)
	}()
	status, err := c.Status(ctx)
	if err != nil {
		return result, err
	}
	result.SourceNewSHA = status.Head
	m, err = a.exportExecutor(ctx, m)
	if err != nil {
		return result, err
	}
	if m.Checkout.Claim.Executor.ExportedHead != result.SourceNewSHA || status.Dirty {
		return result, fmt.Errorf("executor changed during integration; retry after closing writers")
	}
	ancestor, err := mg.IsAncestor(ctx, root, result.TargetOldSHA, result.SourceNewSHA)
	if err != nil {
		return result, err
	}
	if !ancestor {
		return result, fmt.Errorf("target is not an ancestor of returned commit")
	}
	if result.TargetWorktreePath != "" {
		dirty, e := mg.StatusPaths(ctx, result.TargetWorktreePath)
		if e != nil {
			return result, e
		}
		incoming, e := mg.DiffNameOnly(ctx, root, result.TargetOldSHA, result.SourceNewSHA)
		if e != nil {
			return result, e
		}
		if overlap := overlappingPaths(dirty, incoming); len(overlap) > 0 {
			return result, fmt.Errorf("target checkout has overlapping local changes: %s", strings.Join(overlap, ", "))
		}
	}
	if _, err = executor.Git(ctx, root, "update-ref", "refs/heads/"+target, result.SourceNewSHA, result.TargetOldSHA); err != nil {
		return result, err
	}
	result.TargetAdvanced = true
	if result.TargetWorktreePath != "" {
		if err = mg.ReadTreeMergeUpdate(ctx, result.TargetWorktreePath, result.TargetOldSHA, result.SourceNewSHA); err != nil {
			return result, err
		}
		result.TargetSynced = true
	}
	a.appendMergeEvent(ctx, m.ID, "merge.completed", mergeEventData(result, "", ""), &result)
	return result, nil
}
