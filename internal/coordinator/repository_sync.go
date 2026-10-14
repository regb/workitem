package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/lock"
	"github.com/regb/workitem/internal/model"
)

const repositorySyncProjection = "repository_sync"
const repositoryFetchProjection = "repository_fetch"

type RepositorySyncRequest struct {
	Selector            string `json:"selector"`
	Mode                string `json:"mode,omitempty"`
	ConfirmReset        bool   `json:"confirm_reset,omitempty"`
	ExpectedLocalSHA    string `json:"expected_local_sha,omitempty"`
	ExpectedUpstreamSHA string `json:"expected_upstream_sha,omitempty"`
}

type RepositoryStatusRequest struct {
	Selector string `json:"selector"`
	Fetch    bool   `json:"fetch,omitempty"`
}

type RepositorySyncPlan struct {
	ID          string                    `json:"id"`
	Mode        string                    `json:"mode"`
	Phase       string                    `json:"phase"`
	OldSHA      string                    `json:"old_sha"`
	UpstreamSHA string                    `json:"upstream_sha"`
	NewSHA      string                    `json:"new_sha,omitempty"`
	RecoveryRef string                    `json:"recovery_ref"`
	Upstream    *model.RepositoryUpstream `json:"upstream,omitempty"`
}

type RepositoryStatusResult struct {
	Repository         RepositoryView      `json:"repository"`
	LocalSHA           string              `json:"local_sha"`
	UpstreamSHA        string              `json:"upstream_sha,omitempty"`
	State              string              `json:"state"`
	Ahead              int                 `json:"ahead"`
	Behind             int                 `json:"behind"`
	LocalOnlyCommits   []string            `json:"local_only_commits,omitempty"`
	EqualTrees         bool                `json:"equal_trees"`
	FetchedAt          *time.Time          `json:"fetched_at,omitempty"`
	Pending            *RepositorySyncPlan `json:"pending,omitempty"`
	ReconciliationPath string              `json:"reconciliation_path,omitempty"`
}

type RepositorySyncResult struct {
	RepositoryStatusResult
	Changed     bool   `json:"changed"`
	RecoveryRef string `json:"recovery_ref,omitempty"`
}

type repositoryFetch struct {
	SHA      string                   `json:"sha"`
	Upstream model.RepositoryUpstream `json:"upstream"`
	At       time.Time                `json:"at"`
}

func (s *Server) repositoryAccess(ctx context.Context, selector string, refuseBusy bool) (RepositorySourceResult, *lock.FileLock, error) {
	source, err := s.database.RepositorySource(ctx, RepositorySourceRequest{Selector: selector, MetadataOnly: true})
	if err != nil {
		return source, nil, err
	}
	if info, err := os.Lstat(source.GitDir); err != nil {
		return source, nil, err
	} else if !info.IsDir() {
		return source, nil, fmt.Errorf("owned Git store must not be a symlink")
	}
	var guard *lock.FileLock
	if refuseBusy {
		guard, err = lock.TryAcquire(gitpkg.IntegrationLockPath(source.GitDir))
	} else {
		guard, err = lock.Acquire(ctx, gitpkg.IntegrationLockPath(source.GitDir))
	}
	if err == lock.ErrLocked {
		return source, nil, fmt.Errorf("repository integration is active; retry after it finishes")
	}
	if err != nil {
		return source, nil, err
	}
	source, err = s.database.RepositorySource(ctx, RepositorySourceRequest{Selector: source.Repository.ID, MetadataOnly: true})
	if err == nil {
		err = gitpkg.New("git").ValidateOwnedBareObservation(ctx, source.GitDir, source.Repository.IntegrationBranch)
	}
	if err != nil {
		guard.Release()
		return source, nil, err
	}
	return source, guard, nil
}

func (s *Server) repositoryFact(repoID, eventType string, value any, updates ...ProjectionUpdate) error {
	id, err := model.NewID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	_, err = s.database.Commit(Command{ID: id, ProtocolVersion: ProtocolVersion, Type: eventType, Actor: "user", ReceivedAt: now, Payload: payload}, []PendingEvent{{ID: id + ":0", Type: eventType, Timestamp: now, Actor: "user", Payload: payload}}, updates)
	return err
}

func (s *Server) saveSyncPlan(repoID string, plan *RepositorySyncPlan) error {
	update := ProjectionUpdate{Projection: repositorySyncProjection, Key: repoID, Delete: plan == nil}
	if plan != nil {
		update.Value, _ = json.Marshal(plan)
	}
	return s.repositoryFact(repoID, "repository.sync_progress", map[string]any{"repository_id": repoID, "plan": plan}, update)
}

func (s *Server) reconciliationPath(repoID string, plan *RepositorySyncPlan) string {
	return filepath.Join(s.dataRoot, "reconciliation", repoID, plan.ID)
}

func (s *Server) inspectRepository(ctx context.Context, source RepositorySourceResult) (RepositoryStatusResult, error) {
	g := gitpkg.New("git")
	r := RepositoryStatusResult{Repository: source.Repository, State: "untracked"}
	up, err := g.BranchUpstream(ctx, source.GitDir, source.Repository.IntegrationBranch)
	r.Repository.Upstream = up
	r.Repository.UpstreamSource = "git"
	if err != nil {
		r.Repository.UpstreamError = err.Error()
		r.State = "problem"
	}
	r.LocalSHA, err = g.ResolveBranchSHA(ctx, source.GitDir, r.Repository.IntegrationBranch)
	if err != nil {
		return r, err
	}
	var plan RepositorySyncPlan
	if found, err := s.database.ReadProjection(repositorySyncProjection, r.Repository.ID, &plan); err != nil {
		return r, err
	} else if found {
		r.Pending = &plan
		if plan.Mode == "merge" {
			r.ReconciliationPath = s.reconciliationPath(r.Repository.ID, &plan)
		}
	}
	if r.Repository.Upstream == nil || r.Repository.UpstreamError != "" {
		return r, nil
	}
	exists, err := g.ReferenceExists(ctx, source.GitDir, r.Repository.Upstream.Ref())
	if err != nil {
		return r, err
	}
	if !exists {
		r.State = "unfetched"
		return r, nil
	}
	r.UpstreamSHA, err = g.RevParse(ctx, source.GitDir, r.Repository.Upstream.Ref()+"^{commit}")
	if err != nil {
		return r, err
	}
	var observation repositoryFetch
	if found, err := s.database.ReadProjection(repositoryFetchProjection, r.Repository.ID, &observation); err != nil {
		return r, err
	} else if found && observation.SHA == r.UpstreamSHA && observation.Upstream.Ref() == r.Repository.Upstream.Ref() && observation.Upstream.URL != r.Repository.Upstream.URL {
		r.State = "unfetched"
		r.UpstreamSHA = ""
		return r, nil
	} else if found && observation.SHA == r.UpstreamSHA && observation.Upstream == *r.Repository.Upstream {
		r.FetchedAt = &observation.At
	}
	r.Ahead, r.Behind, err = g.AheadBehind(ctx, source.GitDir, r.LocalSHA, r.UpstreamSHA)
	if err != nil {
		return r, err
	}
	switch {
	case r.Ahead == 0 && r.Behind == 0:
		r.State = "equal"
	case r.Ahead == 0:
		r.State = "behind"
	case r.Behind == 0:
		r.State = "ahead"
	default:
		r.State = "diverged"
	}
	if r.Ahead > 0 {
		r.LocalOnlyCommits, err = g.LocalOnlyCommits(ctx, source.GitDir, r.LocalSHA, r.UpstreamSHA)
		if err != nil {
			return r, err
		}
	}
	r.EqualTrees, err = g.EqualTrees(ctx, source.GitDir, r.LocalSHA, r.UpstreamSHA)
	return r, err
}

func (s *Server) fetchRepository(ctx context.Context, source RepositorySourceResult) error {
	g := gitpkg.New("git")
	up, err := g.BranchUpstream(ctx, source.GitDir, source.Repository.IntegrationBranch)
	if err != nil {
		return err
	}
	source.Repository.Upstream = up
	if source.Repository.Upstream == nil {
		return fmt.Errorf("repository %s has no upstream; configure wi repo upstream %s URL --branch BRANCH", source.Repository.Name, source.Repository.Name)
	}
	if err := gitpkg.New("git").FetchRepositoryUpstream(ctx, source.GitDir, *source.Repository.Upstream); err != nil {
		return fmt.Errorf("fetch upstream: %w", err)
	}
	current, err := g.BranchUpstream(ctx, source.GitDir, source.Repository.IntegrationBranch)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(up, current) {
		return fmt.Errorf("Git upstream changed during fetch; integration was not updated")
	}
	sha, err := gitpkg.New("git").RevParse(ctx, source.GitDir, source.Repository.Upstream.Ref()+"^{commit}")
	if err != nil {
		return err
	}
	observation := repositoryFetch{SHA: sha, Upstream: *source.Repository.Upstream, At: time.Now().UTC()}
	data, _ := json.Marshal(observation)
	return s.repositoryFact(source.Repository.ID, "repository.fetched", map[string]any{"repository_id": source.Repository.ID, "upstream": observation.Upstream, "sha": sha}, ProjectionUpdate{Projection: repositoryFetchProjection, Key: source.Repository.ID, Value: data})
}

func (s *Server) repositoryStatus(ctx context.Context, request RepositoryStatusRequest) (RepositoryStatusResult, error) {
	source, guard, err := s.repositoryAccess(ctx, request.Selector, request.Fetch)
	if err != nil {
		return RepositoryStatusResult{}, err
	}
	defer guard.Release()
	if request.Fetch {
		if err := s.fetchRepository(ctx, source); err != nil {
			return RepositoryStatusResult{}, err
		}
	}
	return s.inspectRepository(ctx, source)
}

func (s *Server) repositorySync(ctx context.Context, request RepositorySyncRequest) (RepositorySyncResult, error) {
	result := RepositorySyncResult{}
	mode := request.Mode
	if mode == "" {
		mode = "ff"
	}
	if mode != "ff" && mode != "merge" && mode != "reset" && mode != "continue" && mode != "abort" {
		return result, fmt.Errorf("invalid repository sync mode")
	}
	if mode == "reset" && (!request.ConfirmReset || request.ExpectedLocalSHA == "" || request.ExpectedUpstreamSHA == "") {
		return result, fmt.Errorf("reset requires confirmation and expected local/upstream commits")
	}
	source, guard, err := s.repositoryAccess(ctx, request.Selector, true)
	if err != nil {
		return result, err
	}
	defer guard.Release()
	status, err := s.inspectRepository(ctx, source)
	if err != nil {
		return result, err
	}
	result.RepositoryStatusResult = status
	if status.Pending != nil {
		if mode != "continue" && mode != "abort" {
			return result, fmt.Errorf("repository reconciliation is pending; use wi repo sync %s --continue or --abort", source.Repository.Name)
		}
		return s.resumeRepositorySync(ctx, source, *status.Pending, mode == "abort")
	}
	if mode == "continue" || mode == "abort" {
		return result, fmt.Errorf("repository has no pending reconciliation")
	}
	if err := gitpkg.New("git").ValidateOwnedBare(ctx, source.GitDir, source.Repository.IntegrationBranch); err != nil {
		return result, err
	}
	if err := s.ensureRepositoryIdle(ctx, source, ""); err != nil {
		return result, err
	}
	if err := s.fetchRepository(ctx, source); err != nil {
		return result, err
	}
	status, err = s.inspectRepository(ctx, source)
	result.RepositoryStatusResult = status
	if err != nil {
		return result, err
	}
	if request.ExpectedLocalSHA != "" && status.LocalSHA != request.ExpectedLocalSHA {
		return result, fmt.Errorf("local integration tip changed; inspect and confirm again")
	}
	if request.ExpectedUpstreamSHA != "" && status.UpstreamSHA != request.ExpectedUpstreamSHA {
		return result, fmt.Errorf("upstream tip changed; inspect and confirm again")
	}
	if status.State == "equal" || (status.State == "ahead" && mode != "reset") {
		return result, nil
	}
	if status.State == "diverged" && mode == "ff" {
		hint := ""
		if status.EqualTrees {
			hint = "; file trees are identical but commit histories differ"
		}
		return result, fmt.Errorf("%s diverged from %s (%d local-only, %d upstream-only commits)%s; choose wi repo sync %s --merge or --reset-to-upstream", source.Repository.IntegrationBranch, status.Repository.Upstream.String(), status.Ahead, status.Behind, hint, source.Repository.Name)
	}
	id, err := model.NewID()
	if err != nil {
		return result, err
	}
	plan := RepositorySyncPlan{Upstream: status.Repository.Upstream, ID: id, Mode: mode, OldSHA: status.LocalSHA, UpstreamSHA: status.UpstreamSHA, RecoveryRef: "refs/wi/recovery/repository-sync/" + id, Phase: "prepared", NewSHA: status.UpstreamSHA}
	if mode == "merge" && status.State == "diverged" {
		plan.Phase = "merging"
		plan.NewSHA = ""
	}
	if err := s.saveSyncPlan(source.Repository.ID, &plan); err != nil {
		return result, err
	}
	return s.resumeRepositorySync(ctx, source, plan, false)
}

func (s *Server) ensureRepositoryIdle(ctx context.Context, source RepositorySourceResult, skip string) error {
	worktrees, err := gitpkg.New("git").ListWorktrees(ctx, source.GitDir)
	if err != nil {
		return err
	}
	for _, wt := range worktrees {
		if wt.Bare || wt.Path == skip {
			continue
		}
		if err := gitpkg.New("git").EnsureNoOperationInProgress(ctx, wt.Path, "synchronize repository"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) resumeRepositorySync(ctx context.Context, source RepositorySourceResult, plan RepositorySyncPlan, abort bool) (RepositorySyncResult, error) {
	result := RepositorySyncResult{RecoveryRef: plan.RecoveryRef}
	g := gitpkg.New("git")
	repoID := source.Repository.ID
	ref := "refs/heads/" + source.Repository.IntegrationBranch
	if !model.ValidID(plan.ID) || !gitpkg.ValidCommitSHA(plan.OldSHA) || !gitpkg.ValidCommitSHA(plan.UpstreamSHA) || (plan.NewSHA != "" && !gitpkg.ValidCommitSHA(plan.NewSHA)) || plan.RecoveryRef != "refs/wi/recovery/repository-sync/"+plan.ID || (plan.Mode != "ff" && plan.Mode != "merge" && plan.Mode != "reset") || (plan.Phase != "merging" && plan.Phase != "prepared" && plan.Phase != "applied") || (plan.Phase == "merging" && plan.Mode != "merge") || (plan.Phase != "merging" && plan.NewSHA == "") {
		return result, fmt.Errorf("invalid reconciliation identity")
	}
	path := ""
	if plan.Mode == "merge" {
		path = s.reconciliationPath(repoID, &plan)
	}
	current, err := g.RevParse(ctx, source.GitDir, ref+"^{commit}")
	if err != nil {
		return result, err
	}
	if plan.NewSHA != "" && current == plan.NewSHA {
		plan.Phase = "applied"
	} else if current != plan.OldSHA && !abort && plan.Phase != "applied" {
		return result, fmt.Errorf("integration tip changed during reconciliation; recovery ref %s retained", plan.RecoveryRef)
	}
	if abort && plan.Phase == "applied" {
		return result, fmt.Errorf("integration ref was already updated; use --continue to finish, not --abort")
	}
	if err := s.ensureRepositoryIdle(ctx, source, path); err != nil {
		return result, err
	}
	if path != "" {
		for _, parent := range []string{filepath.Join(s.dataRoot, "reconciliation"), filepath.Dir(path)} {
			if info, err := os.Lstat(parent); err == nil {
				if !info.IsDir() {
					return result, fmt.Errorf("reconciliation parent must not use symlinks")
				}
			} else if !os.IsNotExist(err) {
				return result, err
			}
		}
		if st, err := os.Lstat(path); err == nil {
			if !st.IsDir() {
				return result, fmt.Errorf("reconciliation checkout must not be a symlink")
			}
			info, err := g.DetectRepository(ctx, path, "")
			if err != nil || filepath.Clean(info.Repository.GitCommonDir) != filepath.Clean(source.GitDir) {
				return result, fmt.Errorf("reconciliation checkout no longer belongs to owned store")
			}
			branch, err := g.CurrentBranch(ctx, path)
			if err != nil || branch != "" {
				return result, fmt.Errorf("reconciliation checkout must remain detached")
			}
			if err := g.EnsureReconciliationOperation(ctx, path); err != nil {
				return result, err
			}
			if !abort && plan.NewSHA != "" {
				head, err := g.Head(ctx, path)
				if err != nil {
					return result, err
				}
				if head != plan.NewSHA {
					return result, fmt.Errorf("reconciliation HEAD changed after preparation; retain %s and inspect before continuing", path)
				}
			}
		} else if !os.IsNotExist(err) {
			return result, err
		}
	}
	if abort {
		if path != "" {
			if _, err := os.Stat(path); err == nil {
				merging, err := g.MergeInProgress(ctx, path)
				if err != nil {
					return result, err
				}
				if merging {
					if err := g.AbortReconciliation(ctx, path); err != nil {
						return result, err
					}
				}
				head, err := g.Head(ctx, path)
				if err != nil {
					return result, err
				}
				if head != plan.OldSHA {
					retained := "refs/wi/recovery/repository-reconciliation/" + plan.ID
					if existing, err := g.RevParse(ctx, source.GitDir, retained+"^{commit}"); err != nil {
						if err := g.RecoveryRef(ctx, source.GitDir, retained, head); err != nil {
							return result, err
						}
					} else if existing != head {
						return result, fmt.Errorf("reconciliation recovery ref conflict; retain %s", path)
					}
					result.RecoveryRef = retained
				}
				if err := g.WorktreeRemove(ctx, source.GitDir, path, false); err != nil {
					return result, fmt.Errorf("reconciliation files retained at %s: %w", path, err)
				}
			}
		}
		if err := s.repositoryFact(repoID, "repository.sync_aborted", map[string]any{"repository_id": repoID, "operation_id": plan.ID}, ProjectionUpdate{Projection: repositorySyncProjection, Key: repoID, Delete: true}); err != nil {
			return result, err
		}
		result.RepositoryStatusResult, err = s.inspectRepository(ctx, source)
		return result, err
	}
	if plan.Phase != "applied" && plan.Upstream == nil {
		return result, fmt.Errorf("invalid reconciliation identity: missing upstream snapshot")
	}
	if plan.Phase == "merging" {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return result, err
			}
			if err := g.AddReconciliation(ctx, source.GitDir, path, plan.OldSHA); err != nil {
				return result, err
			}
		}
		merging, err := g.MergeInProgress(ctx, path)
		if err != nil {
			return result, err
		}
		if merging {
			if err := g.CommitReconciliation(ctx, path); err != nil {
				return result, fmt.Errorf("resolve and stage conflicts in %s, then run wi repo sync %s --continue: %w", path, source.Repository.Name, err)
			}
		} else {
			head, err := g.Head(ctx, path)
			if err != nil {
				return result, err
			}
			if head == plan.OldSHA {
				if err := g.MergeReconciliation(ctx, path, plan.UpstreamSHA); err != nil {
					return result, fmt.Errorf("reconciliation retained at %s; resolve and stage conflicts, then wi repo sync %s --continue, or use --abort: %w", path, source.Repository.Name, err)
				}
			}
		}
		if err := g.EnsureClean(ctx, path); err != nil {
			return result, fmt.Errorf("reconciliation checkout must be clean; retain %s: %w", path, err)
		}
		plan.NewSHA, err = g.Head(ctx, path)
		if err != nil {
			return result, err
		}
		for _, ancestor := range []string{plan.OldSHA, plan.UpstreamSHA} {
			ok, err := g.IsAncestor(ctx, source.GitDir, ancestor, plan.NewSHA)
			if err != nil || !ok {
				return result, fmt.Errorf("reconciliation must preserve both local and upstream ancestry")
			}
		}
		plan.Phase = "prepared"
		if err := s.saveSyncPlan(repoID, &plan); err != nil {
			return result, err
		}
	}
	{
		// Recovery refs are created before the compare-and-swap update. A retry
		// accepts only the same protected tip, never overwrites a recovery ref.
		backup, backupErr := g.RevParse(ctx, source.GitDir, plan.RecoveryRef+"^{commit}")
		if backupErr != nil {
			if err := g.RecoveryRef(ctx, source.GitDir, plan.RecoveryRef, plan.OldSHA); err != nil {
				return result, err
			}
		} else if backup != plan.OldSHA {
			return result, fmt.Errorf("recovery ref identity conflict")
		}
	}
	if plan.Mode != "merge" && plan.NewSHA != plan.UpstreamSHA {
		return result, fmt.Errorf("invalid synchronization target")
	}
	if plan.Mode != "reset" && plan.NewSHA != "" {
		for _, ancestor := range []string{plan.OldSHA, plan.UpstreamSHA} {
			ok, err := g.IsAncestor(ctx, source.GitDir, ancestor, plan.NewSHA)
			if err != nil || !ok {
				return result, fmt.Errorf("synchronization must preserve local and upstream ancestry")
			}
		}
	}
	if plan.Phase != "applied" {
		if plan.Upstream != nil {
			current, err := g.BranchUpstream(ctx, source.GitDir, source.Repository.IntegrationBranch)
			if err != nil {
				return result, err
			}
			if !reflect.DeepEqual(current, plan.Upstream) {
				return result, fmt.Errorf("Git upstream changed during reconciliation; restore it or use --abort")
			}
		}
		if err := g.ValidateOwnedBare(ctx, source.GitDir, source.Repository.IntegrationBranch); err != nil {
			return result, err
		}
		if err := s.ensureRepositoryIdle(ctx, source, path); err != nil {
			return result, err
		}
		if err := g.UpdateRef(ctx, source.GitDir, ref, plan.NewSHA, plan.OldSHA, "wi repository sync"); err != nil {
			return result, err
		}
		plan.Phase = "applied"
		if err := s.saveSyncPlan(repoID, &plan); err != nil {
			return result, fmt.Errorf("integration updated; recovery ref %s retained; finish with --continue: %w", plan.RecoveryRef, err)
		}
	}
	result.Changed = true
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if err := g.WorktreeRemove(ctx, source.GitDir, path, false); err != nil {
				return result, fmt.Errorf("integration updated but reconciliation checkout retained at %s; use --continue after inspection: %w", path, err)
			}
		}
	}
	if err := s.repositoryFact(repoID, "repository.synced", map[string]any{"repository_id": repoID, "old_sha": plan.OldSHA, "new_sha": plan.NewSHA, "recovery_ref": plan.RecoveryRef, "mode": plan.Mode}, ProjectionUpdate{Projection: repositorySyncProjection, Key: repoID, Delete: true}); err != nil {
		return result, fmt.Errorf("integration updated to %s; recovery ref %s retained; finish with --continue: %w", plan.NewSHA, plan.RecoveryRef, err)
	}
	result.RepositoryStatusResult, err = s.inspectRepository(ctx, source)
	return result, err
}
