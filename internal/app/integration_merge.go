package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/lock"

	reviewcore "github.com/regb/workitem/internal/app/core/review"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
)

type mergeGit interface {
	DefaultBranch(context.Context, string) (string, error)
	ResolveBranchSHA(context.Context, string, string) (string, error)
	RevParse(context.Context, string, string) (string, error)
	EnsureClean(context.Context, string) error
	EnsureNoUnmerged(context.Context, string) error
	EnsureNoOperationInProgress(context.Context, string, string) error
	RebaseOnto(context.Context, string, string) error
	SquashCommit(context.Context, string, string, string, string) (string, error)
	ReviewTree(context.Context, string, string) (string, error)
	IsAncestor(context.Context, string, string, string) (bool, error)
	UpdateRef(context.Context, string, string, string, string, string) error
	TargetWorktreeForBranch(context.Context, string, string) (*gitpkg.WorktreeInfo, error)
	DiffNameOnly(context.Context, string, string, string) ([]string, error)
	StatusPaths(context.Context, string) ([]string, error)
	IgnoredPaths(context.Context, string) ([]string, error)
	UpdateIndexRefresh(context.Context, string) error
	ReadTreeMergeUpdate(context.Context, string, string, string) error
}

type MergeOptions struct {
	Selector string
	Target   string
	CWD      string
	Env      map[string]string
	Local    bool
	Rebase   bool
	Message  string
	// ByOwnAgent means the item's running agent asked, from inside its
	// turn, which waits on the merge: that turn does not hold it back.
	ByOwnAgent bool
}

type MergeResult struct {
	RevisionID      string `json:"revision_id,omitempty"`
	Commit          string `json:"commit,omitempty"`
	Tree            string `json:"tree,omitempty"`
	SourceCommitted bool   `json:"source_committed"`
	SourceAdvanced  bool   `json:"source_advanced"`
	Recorded        bool   `json:"recorded"`

	UpstreamSync            *coordinator.RepositorySyncResult `json:"upstream_sync,omitempty"`
	WorkItemID              string                            `json:"work_item_id"`
	SourceBranch            string                            `json:"source_branch"`
	SourcePath              string                            `json:"source_path"`
	SourceOldSHA            string                            `json:"source_old_sha"`
	SourceNewSHA            string                            `json:"source_new_sha,omitempty"`
	TargetBranch            string                            `json:"target_branch"`
	TargetOldSHA            string                            `json:"target_old_sha"`
	TargetWorktreePath      string                            `json:"target_worktree_path,omitempty"`
	TargetManagedWorkItemID string                            `json:"target_managed_work_item_id,omitempty"`
	Rebased                 bool                              `json:"rebased"`
	TargetAdvanced          bool                              `json:"target_advanced"`
	TargetSynced            bool                              `json:"target_synced"`
	Warnings                []string                          `json:"warnings"`
}

func (a *App) MergeWorkItem(ctx context.Context, opts MergeOptions) (result MergeResult, resultErr error) {
	if opts.Rebase && opts.Message != "" {
		return MergeResult{}, fmt.Errorf("--message is only used by squash merge")
	}
	input, err := a.captureMergeInput(ctx, opts)
	if err != nil {
		return MergeResult{}, err
	}
	m, operation, err := a.lockItemOperation(ctx, ResolveOptions{Selector: opts.Selector, CWD: opts.CWD, Env: opts.Env})
	if err != nil {
		return MergeResult{}, err
	}
	defer operation.Release()
	if err := a.ensureMergeIdle(ctx, m.ID, opts.ByOwnAgent); err != nil {
		return MergeResult{WorkItemID: m.ID, RevisionID: input.ID}, err
	}
	res := MergeResult{WorkItemID: m.ID, SourceBranch: expectedCheckoutBranch(m), Warnings: []string{}, RevisionID: input.ID}
	if m.State != model.StateActive {
		return res, fmt.Errorf("work item %s is %s; merge requires an active item", m.ID, m.State)
	}
	if !m.Checkout.Present() || m.Checkout.Claim == nil || strings.TrimSpace(m.Checkout.Claim.Path) == "" {
		return res, fmt.Errorf("work item %s has no checkout; start it before merging", m.ID)
	}
	if !reviewClaimMatches(m, input.ClaimID, input.ClaimGeneration) {
		return res, ErrReviewChanged
	}
	res.SourcePath = m.Checkout.Claim.Path
	if input.Head == "" {
		return res, fmt.Errorf("merge input snapshot required")
	}
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
	if err := mg.EnsureNoUnmerged(ctx, res.SourcePath); err != nil {
		return res, err
	}
	repoRoot := m.Repository.OperationalRoot()
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		if m.Repository.ID != "" {
			target = m.Repository.IntegrationBranch
		} else {
			target, err = mg.DefaultBranch(ctx, repoRoot)
			if err != nil {
				return res, err
			}
		}
	}
	if target == res.SourceBranch {
		return res, fmt.Errorf("target branch %s is the source branch", target)
	}
	res.TargetBranch = target
	if m.Repository.ID != "" {
		var source coordinator.RepositorySourceResult
		client := a.coordinatorClient()
		if a.DaemonSocketPath != "" {
			err = client.Call(ctx, coordinator.MethodRepositorySource, coordinator.RepositorySourceRequest{Selector: m.Repository.ID, MetadataOnly: true, ObserveUpstream: true}, &source)
			if err != nil {
				return res, err
			}
			if !opts.Local && source.Repository.UpstreamSource != "git" {
				return res, fmt.Errorf("daemon does not support Git-owned tracking; restart it before merging")
			}
			if source.PendingSync {
				return res, fmt.Errorf("finish or abort pending repository reconciliation before merging")
			}
			if source.Repository.UpstreamError != "" && !opts.Local {
				return res, fmt.Errorf("inspect Git upstream: %s", source.Repository.UpstreamError)
			}
			if source.Repository.Upstream != nil && target == source.Repository.IntegrationBranch && !opts.Local {
				synced, syncErr := client.SyncRepository(ctx, coordinator.RepositorySyncRequest{Selector: m.Repository.ID})
				if syncErr != nil {
					return res, fmt.Errorf("synchronize integration base before merge: %w", syncErr)
				}
				res.UpstreamSync = &synced
			}
		}
		guard, lockErr := lock.Acquire(ctx, gitpkg.IntegrationLockPath(repoRoot))
		if lockErr != nil {
			return res, lockErr
		}
		defer guard.Release()
		if a.DaemonSocketPath != "" {
			var current coordinator.RepositorySourceResult
			err = client.Call(ctx, coordinator.MethodRepositorySource, coordinator.RepositorySourceRequest{Selector: m.Repository.ID, MetadataOnly: true, ObserveUpstream: true}, &current)
			if err != nil {
				return res, err
			}
			if !opts.Local && current.Repository.UpstreamError != "" {
				return res, fmt.Errorf("Git upstream became invalid during integration preflight: %s", current.Repository.UpstreamError)
			}
			if current.PendingSync || !reflect.DeepEqual(current.Repository.Upstream, source.Repository.Upstream) {
				return res, fmt.Errorf("repository synchronization policy changed; retry merge")
			}
			if res.UpstreamSync != nil {
				sha, err := mg.ResolveBranchSHA(ctx, repoRoot, target)
				if err != nil {
					return res, err
				}
				if sha != res.UpstreamSync.LocalSHA {
					return res, fmt.Errorf("integration branch moved after upstream synchronization; retry merge")
				}
			}
		}
		if err := mg.EnsureNoOperationInProgress(ctx, res.SourcePath, "merge"); err != nil {
			return res, err
		}
		if branch, err := a.Git.CurrentBranch(ctx, res.SourcePath); err != nil {
			return res, err
		} else if branch != res.SourceBranch {
			return res, fmt.Errorf("source branch changed during integration preflight; expected %s, found %s", res.SourceBranch, branch)
		}
		if err := mg.EnsureNoUnmerged(ctx, res.SourcePath); err != nil {
			return res, err
		}
	}
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
		if m.Repository.ID != "" && target == m.Repository.IntegrationBranch {
			return res, fmt.Errorf("integration branch %s is checked out at %s; release that checkout before merging", target, targetWorktree.Path)
		}
		if m.Repository.ID != "" && !model.IsManagedSlotPath(a.Store.WorktreesDir(), m.Repository.ID, targetWorktree.Path) {
			return res, fmt.Errorf("target branch %s is checked out in external worktree %s; wi will not modify an unmanaged checkout; choose an unchecked-out target or manage that worktree with Git", target, targetWorktree.Path)
		}
		res.TargetWorktreePath = targetWorktree.Path
		info, statErr := os.Stat(targetWorktree.Path)
		if statErr != nil {
			return res, fmt.Errorf("target branch %s is registered at unavailable worktree %s; run `git worktree prune` or repair it: %w", target, targetWorktree.Path, statErr)
		}
		if !info.IsDir() {
			return res, fmt.Errorf("target branch %s is registered at non-directory worktree path %s; repair the Git worktree registration", target, targetWorktree.Path)
		}
		if err := mg.EnsureNoOperationInProgress(ctx, targetWorktree.Path, "update target checkout"); err != nil {
			return res, err
		}
		if targetItem, err := a.Store.FindByWorktree(targetWorktree.Path); err == nil {
			res.TargetManagedWorkItemID = targetItem.ID
		}
	}

	if res.SourceOldSHA != input.Head {
		return res, fmt.Errorf("source HEAD changed after merge-input capture; target unchanged")
	}
	state, err := a.ReviewRecords.LoadReview(ctx, m.ID)
	if err != nil {
		return res, err
	}
	unresolved := 0
	for _, thread := range state.Threads {
		if thread.Status() != "resolved" && thread.Status() != "withdrawn" {
			unresolved++
		}
	}
	if unresolved > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d unresolved review threads; merge is not blocked", unresolved))
	}
	if len(state.Drafts) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d unpublished review drafts", len(state.Drafts)))
	}
	removed, err := a.removeAttachedCopies(ctx, m)
	if err != nil {
		return res, err
	}
	status, err := a.Git.StatusPorcelain(ctx, res.SourcePath)
	if err != nil {
		return res, err
	}
	if strings.TrimSpace(status) != "" {
		if _, err := a.Git.CommitAll(ctx, res.SourcePath, "wi: uncommitted changes at merge"); err != nil {
			return res, fmt.Errorf("commit merge input: %w", err)
		}
		res.SourceCommitted = true
	}
	source, err := mg.RevParse(ctx, res.SourcePath, "HEAD^{commit}")
	if err != nil {
		return res, err
	}
	delta, err := mg.DiffNameOnly(ctx, repoRoot, input.Commit, source)
	if err != nil {
		return res, err
	}
	allowed := map[string]bool{}
	for _, e := range input.Excluded {
		allowed[e.Path] = true
	}
	for _, p := range removed {
		allowed[p] = true
	}
	for _, p := range delta {
		if !allowed[p] {
			return res, fmt.Errorf("checkout changed after merge-input capture at %s; work retained in source, target unchanged", p)
		}
	}
	if err := mg.EnsureClean(ctx, res.SourcePath); err != nil {
		return res, err
	}
	targetAncestor, err := mg.IsAncestor(ctx, repoRoot, res.TargetOldSHA, source)
	if err != nil {
		return res, err
	}
	if !targetAncestor {
		ignored, err := mg.IgnoredPaths(ctx, res.SourcePath)
		if err != nil {
			return res, err
		}
		incoming, err := mg.DiffNameOnly(ctx, repoRoot, source, res.TargetOldSHA)
		if err != nil {
			return res, err
		}
		if overlap := overlappingPaths(ignored, incoming); len(overlap) > 0 {
			return res, fmt.Errorf("source ignored files overlap rebase input: %s; target unchanged", strings.Join(overlap, ", "))
		}
		if err := mg.RebaseOnto(ctx, res.SourcePath, res.TargetOldSHA); err != nil {
			return res, fmt.Errorf("rebase stopped in %s; target %s is unchanged. Resolve with git rebase --continue or git rebase --abort, then retry wi merge: %w", res.SourcePath, target, err)
		}
		res.Rebased = true
	}
	replayed, err := mg.RevParse(ctx, res.SourcePath, "HEAD^{commit}")
	if err != nil {
		return res, err
	}
	ancestor, err := mg.IsAncestor(ctx, repoRoot, res.TargetOldSHA, replayed)
	if err != nil {
		return res, err
	}
	if !ancestor {
		return res, fmt.Errorf("rebased source cannot fast-forward the fenced target")
	}
	tree, err := mg.ReviewTree(ctx, repoRoot, replayed)
	if err != nil {
		return res, err
	}
	targetTree, err := mg.ReviewTree(ctx, repoRoot, res.TargetOldSHA)
	if err != nil {
		return res, err
	}
	final := replayed
	if !opts.Rebase {
		final = res.TargetOldSHA
		if tree != targetTree {
			message := opts.Message
			if strings.TrimSpace(message) == "" {
				message = m.Title
			}
			final, err = mg.SquashCommit(ctx, repoRoot, tree, res.TargetOldSHA, message)
			if err != nil {
				return res, err
			}
		}
	}
	res.Commit, res.Tree, res.SourceNewSHA = final, tree, final
	if res.TargetWorktreePath != "" {
		dirty, err := mg.StatusPaths(ctx, res.TargetWorktreePath)
		if err != nil {
			return res, err
		}
		ignored, err := mg.IgnoredPaths(ctx, res.TargetWorktreePath)
		if err != nil {
			return res, err
		}
		dirty = append(dirty, ignored...)
		incoming, err := mg.DiffNameOnly(ctx, repoRoot, res.TargetOldSHA, final)
		if err != nil {
			return res, err
		}
		if overlap := overlappingPaths(dirty, incoming); len(overlap) > 0 {
			return res, fmt.Errorf("target checkout has changes overlapping incoming update: %s; target unchanged", strings.Join(overlap, ", "))
		}
	}
	if err := mg.EnsureClean(ctx, res.SourcePath); err != nil {
		return res, err
	}
	currentBranch, branchErr := a.Git.CurrentBranch(ctx, res.SourcePath)
	currentHead, headErr := mg.RevParse(ctx, res.SourcePath, "HEAD^{commit}")
	if branchErr != nil || headErr != nil || currentBranch != res.SourceBranch || currentHead != replayed {
		return res, fmt.Errorf("source branch or HEAD changed before target publication")
	}
	if err := mg.UpdateRef(ctx, repoRoot, "refs/heads/"+target, final, res.TargetOldSHA, "wi merge"); err != nil {
		return res, fmt.Errorf("target changed during merge; source work retained, no target update: %w", err)
	}
	res.TargetAdvanced = final != res.TargetOldSHA
	// Retain successful target publication even if a later record/resource step
	// fails. There is no rollback or automatic replay after this point.
	durableCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	state, err = a.ReviewRecords.LoadReview(durableCtx, m.ID)
	if err != nil {
		return res, fmt.Errorf("target published at %s; load merge evidence: %w", final, err)
	}
	state.Merges = append(state.Merges, reviewcore.Merge{Revision: input.ID, Commit: final, Tree: tree, Target: "refs/heads/" + target, Before: res.TargetOldSHA, After: final, CreatedAt: a.now()})
	state.Base = final
	operationID, err := model.NewID()
	if err != nil {
		return res, err
	}
	if err := a.ReviewRecords.StoreReview(durableCtx, state, state.Version, operationID, "merged", map[string]string{"revision_id": input.ID, "commit": final, "target_before": res.TargetOldSHA, "target_after": final}); err != nil {
		return res, fmt.Errorf("target published at %s; recording merge failed: %w", final, err)
	}
	res.Recorded = true
	if err := mg.EnsureClean(durableCtx, res.SourcePath); err != nil {
		return res, fmt.Errorf("target published at %s; new source changes preserved, continuation not advanced: %w", final, err)
	}
	if replayed != final {
		if err := mg.UpdateRef(durableCtx, repoRoot, "refs/heads/"+res.SourceBranch, final, replayed, "wi merge: continue from result"); err != nil {
			return res, fmt.Errorf("target published at %s; source moved, continuation not updated: %w", final, err)
		}
	}
	res.SourceAdvanced = true
	if res.TargetWorktreePath != "" && res.TargetAdvanced {
		if err := mg.UpdateIndexRefresh(durableCtx, res.TargetWorktreePath); err != nil {
			res.Warnings = append(res.Warnings, "target index refresh: "+err.Error())
		}
		if err := mg.ReadTreeMergeUpdate(durableCtx, res.TargetWorktreePath, res.TargetOldSHA, final); err != nil {
			return res, fmt.Errorf("target published at %s; target checkout synchronization failed: %w", final, err)
		}
		res.TargetSynced = true
		if res.TargetManagedWorkItemID != "" && res.TargetManagedWorkItemID != m.ID {
			data := map[string]any{"source_item_id": m.ID, "target_branch": target, "old_sha": res.TargetOldSHA, "new_sha": final}
			if err := a.Store.AppendEvent(durableCtx, res.TargetManagedWorkItemID, model.NewEvent(a.now(), "checkout.updated_by_merge", "wi", data)); err != nil {
				res.Warnings = append(res.Warnings, "target item event: "+err.Error())
			}
		}
	}
	a.appendMergeEvent(durableCtx, m.ID, "merge.completed", mergeEventData(res, "", ""), &res)
	return res, nil
}

type mergeInput struct {
	reviewcore.Revision
	ClaimID         string
	ClaimGeneration uint64
}

func (a *App) ensureMergeIdle(ctx context.Context, item string, byOwnAgent bool) error {
	if a.ReviewCaptures != nil && a.ReviewCaptures.Capturing(item) {
		return fmt.Errorf("code capture is in progress; wait for capture to finish before merging")
	}
	if byOwnAgent {
		return nil
	}
	runtime, err := a.AgentRuntimeStatus(ctx, ResolveOptions{Selector: item})
	if err != nil {
		return err
	}
	if runtime.Online && a.AgentBusy(ctx, item) {
		return fmt.Errorf("agent is busy running a turn; wait for it to finish before merging, or have the agent run wi merge itself")
	}
	return nil
}
func (a *App) captureMergeInput(ctx context.Context, opts MergeOptions) (mergeInput, error) {
	m, err := a.ResolveItem(ctx, ResolveOptions{Selector: opts.Selector, CWD: opts.CWD, Env: opts.Env})
	if err != nil {
		return mergeInput{}, err
	}
	if m.State != model.StateActive || m.Checkout.Claim == nil {
		return mergeInput{}, fmt.Errorf("merge requires an active item with a checkout")
	}
	branch, err := a.Git.CurrentBranch(ctx, m.Checkout.Claim.Path)
	if err != nil {
		return mergeInput{}, err
	}
	if branch != expectedCheckoutBranch(m) {
		return mergeInput{}, fmt.Errorf("checkout branch mismatch: expected %s, found %s", expectedCheckoutBranch(m), branch)
	}
	if err := a.ensureMergeIdle(ctx, m.ID, opts.ByOwnAgent); err != nil {
		return mergeInput{}, err
	}
	id, err := model.NewID()
	if err != nil {
		return mergeInput{}, err
	}
	claim := m.Checkout.Claim
	result, err := a.CaptureCheckpoint(ctx, CheckpointOptions{ResolveOptions: ResolveOptions{Selector: m.ID}, CaptureID: id, ClaimID: claim.ID, ClaimGeneration: claim.Generation, Trigger: "merge"})
	return mergeInput{Revision: result.Revision, ClaimID: claim.ID, ClaimGeneration: claim.Generation}, err
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
		"rebased": res.Rebased, "target_advanced": res.TargetAdvanced, "target_synced": res.TargetSynced,
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
