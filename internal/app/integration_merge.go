package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
)

type mergeGit interface {
	DefaultBranch(ctx context.Context, repoRoot string) (string, error)
	ResolveBranchSHA(ctx context.Context, repoRoot, branch string) (string, error)
	RevParse(ctx context.Context, dir, rev string) (string, error)
	EnsureClean(ctx context.Context, dir string) error
	EnsureNoOperationInProgress(ctx context.Context, dir, action string) error
	Rebase(ctx context.Context, dir, target string) error
	RebaseAbort(ctx context.Context, dir string) error
	ResetHard(ctx context.Context, dir, rev string) error
	IsAncestor(ctx context.Context, repoRoot, ancestor, descendant string) (bool, error)
	UpdateRef(ctx context.Context, repoRoot, ref, newSHA, oldSHA, message string) error
	TargetWorktreeForBranch(ctx context.Context, repoRoot, branch string) (*gitpkg.WorktreeInfo, error)
	DiffNameOnly(ctx context.Context, repoRoot, oldSHA, newSHA string) ([]string, error)
	StatusPaths(ctx context.Context, dir string) ([]string, error)
	UpdateIndexRefresh(ctx context.Context, dir string) error
	ReadTreeMergeUpdate(ctx context.Context, dir, oldSHA, newSHA string) error
}

type MergeOptions struct {
	Selector string
	Target   string
	CWD      string
	Env      map[string]string
}

type MergeResult struct {
	WorkItemID              string   `json:"work_item_id"`
	SourceBranch            string   `json:"source_branch"`
	SourcePath              string   `json:"source_path"`
	SourceOldSHA            string   `json:"source_old_sha"`
	SourceNewSHA            string   `json:"source_new_sha,omitempty"`
	TargetBranch            string   `json:"target_branch"`
	TargetOldSHA            string   `json:"target_old_sha"`
	TargetWorktreePath      string   `json:"target_worktree_path,omitempty"`
	TargetManagedWorkItemID string   `json:"target_managed_work_item_id,omitempty"`
	Rebased                 bool     `json:"rebased"`
	TargetAdvanced          bool     `json:"target_advanced"`
	TargetSynced            bool     `json:"target_synced"`
	RolledBack              bool     `json:"rolled_back"`
	Warnings                []string `json:"warnings"`
}

type mergeTransaction struct {
	Result MergeResult
}

func (a *App) MergeWorkItem(ctx context.Context, opts MergeOptions) (MergeResult, error) {
	m, err := a.ResolveItem(ctx, ResolveOptions{Selector: opts.Selector, CWD: opts.CWD, Env: opts.Env})
	if err != nil {
		return MergeResult{}, err
	}
	res := MergeResult{WorkItemID: m.ID, SourceBranch: expectedCheckoutBranch(m), Warnings: []string{}}
	if m.Checkout.Kind == model.WorkspaceKindRepositoryHome {
		return res, fmt.Errorf("repository-home work item is already on the local default branch; commit and push with normal Git commands instead of `wi merge`")
	}
	if m.State != model.StateWorking && m.State != model.StateWaiting {
		return res, fmt.Errorf("work item %s is %s; merge requires a working or waiting item", m.ID, m.State)
	}
	if !m.Checkout.Present() || m.Checkout.Path == nil || strings.TrimSpace(*m.Checkout.Path) == "" {
		return res, fmt.Errorf("work item %s has no checkout; start it before merging", m.ID)
	}
	res.SourcePath = *m.Checkout.Path
	mg, ok := a.Git.(mergeGit)
	if !ok {
		return res, fmt.Errorf("configured Git adapter does not support merge operations")
	}
	if err := mg.EnsureNoOperationInProgress(ctx, res.SourcePath, "merge"); err != nil {
		return res, err
	}
	branch, err := a.Git.CurrentBranch(ctx, res.SourcePath)
	if err != nil {
		return res, fmt.Errorf("inspect source branch: %w", err)
	}
	if branch == "" {
		return res, fmt.Errorf("source checkout is detached; expected branch %s", res.SourceBranch)
	}
	if branch != res.SourceBranch {
		return res, fmt.Errorf("checkout branch mismatch at %s: expected %s, found %s; repair the worktree before continuing", res.SourcePath, res.SourceBranch, branch)
	}
	if err := mg.EnsureClean(ctx, res.SourcePath); err != nil {
		return res, fmt.Errorf("source checkout must be completely clean before merge: %w", err)
	}
	repoRoot := m.Repository.RootAtCreation
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		target, err = mg.DefaultBranch(ctx, repoRoot)
		if err != nil {
			return res, err
		}
	}
	if target == res.SourceBranch {
		return res, fmt.Errorf("target branch %s is the source branch", target)
	}
	res.TargetBranch = target
	res.SourceOldSHA, err = mg.RevParse(ctx, res.SourcePath, "HEAD^{commit}")
	if err != nil {
		return res, fmt.Errorf("resolve source HEAD: %w", err)
	}
	res.TargetOldSHA, err = mg.ResolveBranchSHA(ctx, repoRoot, target)
	if err != nil {
		return res, fmt.Errorf("target must be an existing local branch %q: %w", target, err)
	}
	targetWorktree, err := mg.TargetWorktreeForBranch(ctx, repoRoot, target)
	if err != nil {
		return res, fmt.Errorf("inspect target worktree: %w", err)
	}
	if targetWorktree != nil {
		res.TargetWorktreePath = targetWorktree.Path
		info, statErr := os.Stat(targetWorktree.Path)
		if statErr != nil {
			return res, fmt.Errorf("target branch %s is registered at unavailable worktree %s; run `git worktree prune` or repair it: %w", target, targetWorktree.Path, statErr)
		}
		if !info.IsDir() {
			return res, fmt.Errorf("target branch %s is registered at non-directory worktree path %s; repair the Git worktree registration", target, targetWorktree.Path)
		}
		if targetItem, err := a.Store.FindByWorktree(targetWorktree.Path); err == nil {
			res.TargetManagedWorkItemID = targetItem.ID
		}
	}

	tx := &mergeTransaction{Result: res}
	a.appendMergeEvent(ctx, m.ID, "merge.started", mergeEventData(tx.Result, "", ""), &tx.Result)
	if err := mg.Rebase(ctx, res.SourcePath, target); err != nil {
		abortErr := mg.RebaseAbort(ctx, res.SourcePath)
		head, headErr := mg.RevParse(ctx, res.SourcePath, "HEAD^{commit}")
		if abortErr != nil {
			tx.Result.Warnings = append(tx.Result.Warnings, "automatic rebase abort failed: "+abortErr.Error())
		}
		headRestored := headErr == nil && head == res.SourceOldSHA
		if !headRestored {
			tx.Result.Warnings = append(tx.Result.Warnings, fmt.Sprintf("automatic abort did not verify source HEAD %s; inspect %s manually", shortSHA(res.SourceOldSHA), res.SourcePath))
		}
		message := fmt.Sprintf("rebase of %s onto %s failed and was aborted; no merge was performed. Run `git -C %s rebase %s` manually, resolve conflicts, then retry `wi merge`", res.SourceBranch, target, res.SourcePath, target)
		if abortErr != nil {
			message = fmt.Sprintf("rebase of %s onto %s failed and automatic abort also failed; inspect %s manually", res.SourceBranch, target, res.SourcePath)
		} else if !headRestored {
			message += fmt.Sprintf(". WARNING: source HEAD was not verified at %s; inspect it manually", shortSHA(res.SourceOldSHA))
		}
		a.appendMergeEvent(ctx, m.ID, "merge.failed", mergeEventData(tx.Result, "rebase", err.Error()), &tx.Result)
		tx.Result.RolledBack = abortErr == nil && headRestored
		rollbackData := mergeEventData(tx.Result, "rebase", err.Error())
		if abortErr != nil {
			rollbackData["rollback_errors"] = []string{abortErr.Error()}
		}
		a.appendMergeEvent(ctx, m.ID, "merge.rolled_back", rollbackData, &tx.Result)
		return tx.Result, errors.New(message)
	}
	tx.Result.Rebased = true
	tx.Result.SourceNewSHA, err = mg.RevParse(ctx, res.SourcePath, "HEAD^{commit}")
	if err != nil {
		return a.failMergeWithRollback(ctx, m.ID, mg, tx, "resolve_rebased_head", err)
	}
	a.appendMergeEvent(ctx, m.ID, "merge.rebased", mergeEventData(tx.Result, "", ""), &tx.Result)
	ancestor, err := mg.IsAncestor(ctx, repoRoot, res.TargetOldSHA, tx.Result.SourceNewSHA)
	if err != nil {
		return a.failMergeWithRollback(ctx, m.ID, mg, tx, "fast_forward_check", err)
	}
	if !ancestor {
		return a.failMergeWithRollback(ctx, m.ID, mg, tx, "fast_forward_check", fmt.Errorf("target %s cannot fast-forward to rebased source", target))
	}
	if res.TargetWorktreePath != "" {
		dirty, err := mg.StatusPaths(ctx, res.TargetWorktreePath)
		if err != nil {
			return a.failMergeWithRollback(ctx, m.ID, mg, tx, "target_dirty_check", err)
		}
		incoming, err := mg.DiffNameOnly(ctx, repoRoot, res.TargetOldSHA, tx.Result.SourceNewSHA)
		if err != nil {
			return a.failMergeWithRollback(ctx, m.ID, mg, tx, "incoming_paths", err)
		}
		if overlap := overlappingPaths(dirty, incoming); len(overlap) > 0 {
			return a.failMergeWithRollback(ctx, m.ID, mg, tx, "target_overlap", fmt.Errorf("target worktree has local changes overlapping incoming update:\n  %s", strings.Join(overlap, "\n  ")))
		}
	}
	ref := "refs/heads/" + target
	if err := mg.UpdateRef(ctx, repoRoot, ref, tx.Result.SourceNewSHA, res.TargetOldSHA, "wi merge: fast-forward"); err != nil {
		return a.failMergeWithRollback(ctx, m.ID, mg, tx, "target_update", fmt.Errorf("target branch %s moved during merge: %w", target, err))
	}
	tx.Result.TargetAdvanced = true
	a.appendMergeEvent(ctx, m.ID, "merge.target_advanced", mergeEventData(tx.Result, "", ""), &tx.Result)
	if res.TargetWorktreePath != "" {
		if err := mg.UpdateIndexRefresh(ctx, res.TargetWorktreePath); err != nil {
			tx.Result.Warnings = append(tx.Result.Warnings, "target index refresh reported local changes: "+err.Error())
		}
		if err := mg.ReadTreeMergeUpdate(ctx, res.TargetWorktreePath, res.TargetOldSHA, tx.Result.SourceNewSHA); err != nil {
			return a.failMergeWithRollback(ctx, m.ID, mg, tx, "target_sync", fmt.Errorf("sync target worktree %s: %w", res.TargetWorktreePath, err))
		}
		tx.Result.TargetSynced = true
		a.appendMergeEvent(ctx, m.ID, "merge.target_synced", mergeEventData(tx.Result, "", ""), &tx.Result)
		if res.TargetManagedWorkItemID != "" && res.TargetManagedWorkItemID != m.ID {
			data := map[string]any{"source_item_id": m.ID, "target_branch": target, "old_sha": res.TargetOldSHA, "new_sha": tx.Result.SourceNewSHA}
			if err := a.Store.AppendEvent(ctx, res.TargetManagedWorkItemID, model.NewEvent(a.now(), "checkout.updated_by_merge", "wi", data)); err != nil {
				tx.Result.Warnings = append(tx.Result.Warnings, "could not append target work-item event: "+err.Error())
			}
		}
	}
	currentTarget, verifyErr := mg.ResolveBranchSHA(ctx, repoRoot, target)
	if verifyErr != nil || currentTarget != tx.Result.SourceNewSHA {
		tx.Result.Warnings = append(tx.Result.Warnings, fmt.Sprintf("target branch moved again after merge; inspect %s and %s", target, res.TargetWorktreePath))
	}
	a.appendMergeEvent(ctx, m.ID, "merge.completed", mergeEventData(tx.Result, "", ""), &tx.Result)
	return tx.Result, nil
}

func (a *App) failMergeWithRollback(ctx context.Context, itemID string, mg mergeGit, tx *mergeTransaction, stage string, cause error) (MergeResult, error) {
	a.appendMergeEvent(ctx, itemID, "merge.failed", mergeEventData(tx.Result, stage, cause.Error()), &tx.Result)
	rollbackErrors := []string{}
	if tx.Result.TargetAdvanced {
		repoRoot := ""
		if m, err := a.Store.LoadManifest(itemID); err == nil {
			repoRoot = m.Repository.RootAtCreation
		}
		if repoRoot == "" {
			rollbackErrors = append(rollbackErrors, "could not resolve repository root for target rollback")
		} else if err := mg.UpdateRef(ctx, repoRoot, "refs/heads/"+tx.Result.TargetBranch, tx.Result.TargetOldSHA, tx.Result.SourceNewSHA, "wi merge: rollback target sync failed"); err != nil {
			rollbackErrors = append(rollbackErrors, "target ref rollback failed: "+err.Error())
		} else {
			tx.Result.TargetAdvanced = false
		}
	}
	if tx.Result.Rebased {
		if err := mg.ResetHard(ctx, tx.Result.SourcePath, tx.Result.SourceOldSHA); err != nil {
			rollbackErrors = append(rollbackErrors, "source reset failed: "+err.Error())
		} else {
			tx.Result.Rebased = false
		}
	}
	tx.Result.RolledBack = len(rollbackErrors) == 0
	rollbackData := mergeEventData(tx.Result, stage, cause.Error())
	rollbackData["rollback_errors"] = rollbackErrors
	a.appendMergeEvent(ctx, itemID, "merge.rolled_back", rollbackData, &tx.Result)
	if len(rollbackErrors) > 0 {
		return tx.Result, fmt.Errorf("merge failed during %s: %v; rollback incomplete: %s", stage, cause, strings.Join(rollbackErrors, "; "))
	}
	return tx.Result, fmt.Errorf("merge failed during %s: %v; no merge was performed; source and target were restored to their previous commits", stage, cause)
}

func (a *App) appendMergeEvent(ctx context.Context, itemID, eventType string, data map[string]any, res *MergeResult) {
	if err := a.Store.AppendEvent(ctx, itemID, model.NewEvent(a.now(), eventType, "user", data)); err != nil {
		res.Warnings = append(res.Warnings, "could not append "+eventType+" event: "+err.Error())
	}
}

func mergeEventData(res MergeResult, stage, failure string) map[string]any {
	data := map[string]any{
		"source_branch": res.SourceBranch, "source_path": res.SourcePath, "source_old_sha": res.SourceOldSHA,
		"source_new_sha": res.SourceNewSHA, "target_branch": res.TargetBranch, "target_old_sha": res.TargetOldSHA,
		"target_new_sha": res.SourceNewSHA, "target_worktree_path": res.TargetWorktreePath,
		"rebased": res.Rebased, "target_advanced": res.TargetAdvanced, "target_synced": res.TargetSynced, "rolled_back": res.RolledBack,
	}
	if stage != "" {
		data["failure_stage"] = stage
	}
	if failure != "" {
		data["error"] = failure
	}
	return data
}

func overlappingPaths(dirty, incoming []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, dirtyPath := range dirty {
		dirtyPath = filepath.ToSlash(filepath.Clean(dirtyPath))
		for _, incomingPath := range incoming {
			incomingPath = filepath.ToSlash(filepath.Clean(incomingPath))
			if dirtyPath == incomingPath || strings.HasPrefix(dirtyPath, incomingPath+"/") || strings.HasPrefix(incomingPath, dirtyPath+"/") {
				if !seen[dirtyPath] {
					seen[dirtyPath] = true
					result = append(result, dirtyPath)
				}
				break
			}
		}
	}
	sort.Strings(result)
	return result
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
