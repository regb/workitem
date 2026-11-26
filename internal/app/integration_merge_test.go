package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	reviewcore "github.com/regb/workitem/internal/app/core/review"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

type mergeRecords struct {
	mu        sync.Mutex
	state     reviewcore.State
	failMerge bool
}

func (r *mergeRecords) LoadReview(context.Context, string) (reviewcore.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, _ := json.Marshal(r.state)
	var state reviewcore.State
	_ = json.Unmarshal(b, &state)
	return state, nil
}
func (r *mergeRecords) StoreReview(_ context.Context, s reviewcore.State, expected uint64, operation, kind string, ids map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if kind == "merged" && r.failMerge {
		return errors.New("merge record failed")
	}
	if r.state.Version != expected {
		return errors.New("stale")
	}
	s.Version = expected + 1
	b, _ := json.Marshal(s)
	return json.Unmarshal(b, &r.state)
}
func TestMergeSquashesDirtyAndCommittedWorkAndAdvancesContinuation(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, source, "feature.txt", "first\n")
	runMergeGit(t, source, "add", ".")
	runMergeGit(t, source, "commit", "-m", "first")
	beforeInput := mergeGitOutput(t, source, "rev-parse", "HEAD")
	writeMergeFile(t, source, "feature.txt", "second uncommitted\n")
	writeMergeFile(t, repo, "main.txt", "upstream\n")
	runMergeGit(t, repo, "add", ".")
	runMergeGit(t, repo, "commit", "-m", "target advanced")
	before := mergeGitOutput(t, repo, "rev-parse", "main")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err != nil {
		t.Fatal(err)
	}
	if !result.SourceCommitted || !result.Rebased || !result.SourceAdvanced || !result.Recorded {
		t.Fatal(result)
	}
	after := mergeGitOutput(t, repo, "rev-parse", "main")
	if mergeGitOutput(t, repo, "rev-parse", after+"^") != before || mergeGitOutput(t, source, "rev-parse", "HEAD") != after {
		t.Fatal("wrong squash parent or continuation")
	}
	if mergeGitOutput(t, repo, "show", "main:feature.txt") != "second uncommitted" {
		t.Fatal("dirty work not integrated")
	}
	if mergeGitOutput(t, repo, "log", "-1", "--format=%s", after) != "Merge Feature" {
		t.Fatal("squash did not default to title")
	}
	if mergeGitOutput(t, source, "status", "--porcelain") != "" {
		t.Fatal("continued checkout dirty")
	}
	records, _ := a.ReviewRecords.LoadReview(context.Background(), item)
	input := records.Revisions[0]
	if input.Head != beforeInput || input.Trigger != "merge" || records.Base != after || records.Merges[0].Revision != input.ID || records.Merges[0].Tree != result.Tree {
		t.Fatal("merge evidence did not separate input and result", records)
	}
	cmd := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", input.Commit, after)
	if err := cmd.Run(); err == nil {
		t.Fatal("private snapshot ancestry exported")
	}
	writeMergeFile(t, source, "later.txt", "next work\n")
	second, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Message: "Next squash"})
	if err != nil {
		t.Fatal(err)
	}
	records, _ = a.ReviewRecords.LoadReview(context.Background(), item)
	if records.Revisions[1].Base != after || records.Revisions[0].Base == after || records.Base != second.Commit {
		t.Fatal("merge boundary did not establish next base")
	}
}
func TestMergeRebasePreservesSeries(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	base := mergeGitOutput(t, repo, "rev-parse", "main")
	for _, name := range []string{"one", "two"} {
		writeMergeFile(t, source, name, name)
		runMergeGit(t, source, "add", ".")
		runMergeGit(t, source, "commit", "-m", name)
	}
	tip := mergeGitOutput(t, source, "rev-parse", "HEAD")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Rebase: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Commit != tip || mergeGitOutput(t, repo, "rev-list", "--count", base+"..main") != "2" {
		t.Fatal("series flattened or replaced", result)
	}
}
func TestMergeAlreadyContainedDoesNotCreateEmptyCommit(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, repo, "new.txt", "target work")
	runMergeGit(t, repo, "add", ".")
	runMergeGit(t, repo, "commit", "-m", "advance")
	before := mergeGitOutput(t, repo, "rev-parse", "main")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetAdvanced || result.Commit != before || mergeGitOutput(t, source, "rev-parse", "HEAD") != before {
		t.Fatal("contained merge made an empty commit", result)
	}
}
func TestMergeRebaseConflictLeavesCheckoutResolvableAndTargetUntouched(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, source, "README.md", "source conflict\n")
	writeMergeFile(t, repo, "README.md", "target conflict\n")
	runMergeGit(t, repo, "add", ".")
	runMergeGit(t, repo, "commit", "-m", "target")
	before := mergeGitOutput(t, repo, "rev-parse", "main")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err == nil || !result.SourceCommitted || result.TargetAdvanced {
		t.Fatal("conflict lost retained source or changed target", result, err)
	}
	if mergeGitOutput(t, repo, "rev-parse", "main") != before || !strings.Contains(mergeGitOutput(t, source, "status", "--porcelain"), "UU") {
		t.Fatal("conflict was aborted or target moved")
	}
	runMergeGit(t, source, "rebase", "--abort")
}

type mergeCASFault struct {
	gitpkg.Client
	ref    string
	failed bool
}

func (g *mergeCASFault) UpdateRef(ctx context.Context, dir, ref, newSHA, oldSHA, message string) error {
	if ref == g.ref && !g.failed {
		g.failed = true
		return errors.New("simulated ref movement")
	}
	return g.Client.UpdateRef(ctx, dir, ref, newSHA, oldSHA, message)
}
func TestMergeTargetAndSourceCASFailuresRetainSuccessfulSteps(t *testing.T) {
	for _, sourceFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(sourceFailure), func(t *testing.T) {
			a, repo, source, item := setupMergeWorkItem(t)
			writeMergeFile(t, source, "work.txt", "work")
			branch := mergeGitOutput(t, source, "branch", "--show-current")
			ref := "refs/heads/main"
			if sourceFailure {
				ref = "refs/heads/" + branch
			}
			a.Git = &mergeCASFault{Client: gitpkg.New("git"), ref: ref}
			before := mergeGitOutput(t, repo, "rev-parse", "main")
			result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
			if err == nil || !result.SourceCommitted {
				t.Fatal(result, err)
			}
			records, _ := a.ReviewRecords.LoadReview(context.Background(), item)
			if sourceFailure {
				if !result.TargetAdvanced || !result.Recorded || len(records.Merges) != 1 || mergeGitOutput(t, repo, "rev-parse", "main") == before {
					t.Fatal("successful target was rolled back")
				}
				published := result.Commit
				retry, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
				if err != nil || retry.Commit != published {
					t.Fatal("explicit retry created another target commit", retry, err)
				}
			} else if result.TargetAdvanced || len(records.Merges) != 0 || mergeGitOutput(t, repo, "rev-parse", "main") != before {
				t.Fatal("failed target CAS published")
			}
		})
	}
}
func TestMergeRecordFailureDoesNotRollBackPublishedTarget(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, source, "work.txt", "work")
	a.ReviewRecords.(*mergeRecords).failMerge = true
	before := mergeGitOutput(t, repo, "rev-parse", "main")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err == nil || !result.TargetAdvanced || result.Recorded || mergeGitOutput(t, repo, "rev-parse", "main") == before {
		t.Fatal("lost durable target step", result, err)
	}
}
func TestMergeKeepsItemBranchSemantics(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	runMergeGit(t, source, "checkout", "-b", "other")
	before := mergeGitOutput(t, repo, "rev-parse", "main")
	if _, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item}); err == nil {
		t.Fatal("switched checkout was merged")
	}
	if mergeGitOutput(t, repo, "rev-parse", "main") != before {
		t.Fatal("target moved")
	}
}
func setupMergeWorkItem(t *testing.T) (*app.App, string, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	runMergeGit(t, repo, "init", "-b", "main")
	runMergeGit(t, repo, "config", "user.email", "test@example.com")
	runMergeGit(t, repo, "config", "user.name", "Test User")
	writeMergeFile(t, repo, "README.md", "initial\n")
	runMergeGit(t, repo, "add", "README.md")
	runMergeGit(t, repo, "commit", "-m", "initial")
	st := store.New(t.TempDir())
	application := newTestApp(st, gitpkg.New("git"))
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	sequenceIDs(t, application, testutil.ID(t, 91), testutil.ID(t, 92))
	created, _ := startItem(t, application, app.NewWorkItemOptions{Title: "Merge Feature", CWD: repo}, agent.ModeTUI)
	source := created.Checkout.Claim.Path
	runMergeGit(t, source, "config", "user.email", "test@example.com")
	runMergeGit(t, source, "config", "user.name", "Test User")
	m, _ := application.Store.LoadManifest(created.ID)
	m.Repository.CurrentRoot = filepath.Join(repo, ".git")
	if err := application.Store.SaveManifest(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	application.ReviewRecords = &mergeRecords{state: reviewcore.New(m.ID, m.Repository.ID)}
	return application, repo, source, created.ID
}

func writeMergeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readMergeFile(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func runMergeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func mergeGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

type mergeNoRebase struct {
	gitpkg.Client
	called bool
}

func (g *mergeNoRebase) RebaseOnto(context.Context, string, string) error {
	g.called = true
	return errors.New("unexpected rebase")
}

type mergeAncestryFailure struct{ gitpkg.Client }

func (mergeAncestryFailure) IsAncestor(context.Context, string, string, string) (bool, error) {
	return false, errors.New("injected ancestry check failure")
}
func TestMergeFailsClosedWhenAncestryCheckFails(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	a.Git = mergeAncestryFailure{gitpkg.New("git")}
	head := mergeGitOutput(t, source, "rev-parse", "HEAD")
	target := mergeGitOutput(t, repo, "rev-parse", "main")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Rebase: true})
	if err == nil || !strings.Contains(err.Error(), "ancestry check failure") || result.TargetAdvanced || result.Rebased {
		t.Fatal(result, err)
	}
	if mergeGitOutput(t, source, "rev-parse", "HEAD") != head || mergeGitOutput(t, repo, "rev-parse", "main") != target {
		t.Fatal("ancestry failure changed refs")
	}
}
func TestMergeMissingRegisteredTargetRefusesBeforeRebase(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	target := filepath.Join(t.TempDir(), "missing")
	runMergeGit(t, repo, "worktree", "add", "-b", "develop", target, "main")
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	writeMergeFile(t, source, "feature.txt", "feature")
	runMergeGit(t, source, "add", ".")
	runMergeGit(t, source, "commit", "-m", "feature")
	head := mergeGitOutput(t, source, "rev-parse", "HEAD")
	g := &mergeNoRebase{Client: gitpkg.New("git")}
	a.Git = g
	_, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Target: "develop"})
	if err == nil || !strings.Contains(err.Error(), "unavailable worktree") || g.called {
		t.Fatal("missing worktree was not rejected before rebase", err)
	}
	if mergeGitOutput(t, source, "rev-parse", "HEAD") != head {
		t.Fatal("missing target changed source")
	}
}
func TestMergeSyncsManagedTargetWorktreeAndRecordsItsEvent(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	next := 110
	a.NewID = func() (string, error) { next++; return testutil.ID(t, byte(next)), nil }
	target, _ := startItem(t, a, app.NewWorkItemOptions{Title: "Managed target", CWD: repo}, agent.ModeTUI)
	targetPath := target.Checkout.Claim.Path
	branch := target.Checkout.Specification.Branch
	writeMergeFile(t, targetPath, "target.txt", "target commit\n")
	runMergeGit(t, targetPath, "add", ".")
	runMergeGit(t, targetPath, "commit", "-m", "target change")
	writeMergeFile(t, targetPath, "local.txt", "keep dirty target\n")
	writeMergeFile(t, source, "feature.txt", "feature\n")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Target: branch})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Rebased || !result.TargetSynced || result.TargetManagedWorkItemID != target.ID || readMergeFile(t, targetPath, "feature.txt") != "feature\n" || readMergeFile(t, targetPath, "local.txt") != "keep dirty target\n" {
		t.Fatal("managed target not synchronized safely", result)
	}
	events, err := a.Store.(*store.Store).ReadEvents(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type == "checkout.updated_by_merge" {
			found = true
			if event.Data["source_item_id"] != item || event.Data["new_sha"] != result.Commit {
				t.Fatal("incorrect managed target event", event)
			}
		}
	}
	if !found {
		t.Fatal("managed target event missing")
	}
}
func TestMergeExplicitTargetWithoutWorktree(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	runMergeGit(t, repo, "branch", "develop")
	writeMergeFile(t, source, "feature.txt", "feature")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Target: "develop"})
	if err != nil || !result.TargetAdvanced || result.TargetSynced || result.TargetWorktreePath != "" {
		t.Fatal(result, err)
	}
}
func TestMergeRebaseFastForwardPreservesSubtreeMerge(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	g := &mergeNoRebase{Client: gitpkg.New("git")}
	a.Git = g
	writeMergeFile(t, source, "BUILD.bazel", "monorepo build\n")
	runMergeGit(t, source, "add", ".")
	runMergeGit(t, source, "commit", "-m", "monorepo build")
	vendor := t.TempDir()
	runMergeGit(t, vendor, "init", "-b", "main")
	runMergeGit(t, vendor, "config", "user.name", "Test")
	runMergeGit(t, vendor, "config", "user.email", "test@example.invalid")
	writeMergeFile(t, vendor, "BUILD.bazel", "vendor build\n")
	runMergeGit(t, vendor, "add", ".")
	runMergeGit(t, vendor, "commit", "-m", "vendor root")
	runMergeGit(t, source, "fetch", vendor, "main")
	runMergeGit(t, source, "merge", "--allow-unrelated-histories", "-s", "ours", "--no-commit", "FETCH_HEAD")
	runMergeGit(t, source, "read-tree", "--prefix=vendor/open3/", "-u", "FETCH_HEAD")
	runMergeGit(t, source, "commit", "-m", "Vendor subtree")
	head := mergeGitOutput(t, source, "rev-parse", "HEAD")
	parents := mergeGitOutput(t, source, "show", "-s", "--format=%P", "HEAD")
	if len(strings.Fields(parents)) != 2 {
		t.Fatal("not a subtree merge")
	}
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Rebase: true})
	if err != nil || result.Commit != head || result.Rebased || g.called || !result.TargetSynced {
		t.Fatal(result, err)
	}
	if mergeGitOutput(t, repo, "show", "-s", "--format=%P", "main") != parents {
		t.Fatal("subtree parents changed")
	}
	for _, root := range []string{repo, source} {
		if readMergeFile(t, root, "BUILD.bazel") != "monorepo build\n" || readMergeFile(t, root, "vendor/open3/BUILD.bazel") != "vendor build\n" {
			t.Fatal("subtree content changed")
		}
	}
}

type mergeSyncFailure struct{ gitpkg.Client }

func (mergeSyncFailure) ReadTreeMergeUpdate(context.Context, string, string, string) error {
	return errors.New("injected target sync failure")
}
func TestMergeTargetSyncFailureRetainsPublicationAndSource(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, source, "feature.txt", "retained")
	a.Git = mergeSyncFailure{gitpkg.New("git")}
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err == nil || !result.TargetAdvanced || !result.Recorded || !result.SourceAdvanced {
		t.Fatal(result, err)
	}
	if mergeGitOutput(t, repo, "rev-parse", "main") != result.Commit || mergeGitOutput(t, source, "rev-parse", "HEAD") != result.Commit {
		t.Fatal("published result rolled back")
	}
}
func TestMergeRefusesCaptureInProgress(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	a.ReviewCaptures = app.NewReviewCaptureQueue(ctx, func(ctx context.Context, _ string) { close(entered); <-ctx.Done() })
	defer func() { cancel(); a.ReviewCaptures.Close() }()
	a.ReviewCaptures.Trigger(item)
	<-entered
	target := mergeGitOutput(t, repo, "rev-parse", "main")
	_, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	cancel()
	if err == nil || !strings.Contains(err.Error(), "capture is in progress") {
		t.Fatal(err)
	}
	if mergeGitOutput(t, source, "rev-parse", "HEAD") != target {
		t.Fatal("capture refusal changed source")
	}
}
func TestMergeRejectsUnmergedIndexWithoutOperationMarker(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	base := mergeGitOutput(t, source, "rev-parse", "HEAD")
	writeMergeFile(t, source, "README.md", "source\n")
	runMergeGit(t, source, "add", ".")
	runMergeGit(t, source, "commit", "-m", "source")
	writeMergeFile(t, repo, "README.md", "target\n")
	runMergeGit(t, repo, "add", ".")
	runMergeGit(t, repo, "commit", "-m", "target")
	runMergeGit(t, source, "read-tree", "-m", base, "HEAD", "main")
	before := mergeGitOutput(t, source, "ls-files", "--stage")
	if _, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item}); err == nil {
		t.Fatal("unmerged index accepted")
	}
	if mergeGitOutput(t, source, "ls-files", "--stage") != before {
		t.Fatal("unmerged index changed")
	}
}

func TestMergeProtectsIgnoredSourceAndDirtyTargetFiles(t *testing.T) {
	t.Run("ignored source", func(t *testing.T) {
		a, repo, source, item := setupMergeWorkItem(t)
		writeMergeFile(t, source, ".gitignore", "ignored.txt\n")
		runMergeGit(t, source, "add", ".gitignore")
		runMergeGit(t, source, "commit", "-m", "ignore local data")
		writeMergeFile(t, source, "ignored.txt", "local data\n")
		writeMergeFile(t, repo, "ignored.txt", "target data\n")
		runMergeGit(t, repo, "add", "ignored.txt")
		runMergeGit(t, repo, "commit", "-m", "start tracking path")
		before := mergeGitOutput(t, repo, "rev-parse", "main")
		if _, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item}); err == nil {
			t.Fatal("ignored source data overwritten")
		}
		if readMergeFile(t, source, "ignored.txt") != "local data\n" || mergeGitOutput(t, repo, "rev-parse", "main") != before {
			t.Fatal("unsafe rebase changed data")
		}
	})
	for _, overlap := range []bool{false, true} {
		name := "unrelated"
		if overlap {
			name = "overlap"
		}
		t.Run(name, func(t *testing.T) {
			a, repo, source, item := setupMergeWorkItem(t)
			writeMergeFile(t, source, "README.md", "source work\n")
			path := "local-note"
			if overlap {
				path = "README.md"
			}
			writeMergeFile(t, repo, path, "keep target work\n")
			before := mergeGitOutput(t, repo, "rev-parse", "main")
			_, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
			if overlap {
				if err == nil || mergeGitOutput(t, repo, "rev-parse", "main") != before {
					t.Fatal("overlapping dirty target changed")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if readMergeFile(t, repo, path) != "keep target work\n" {
				t.Fatal("target local work lost")
			}
		})
	}
}
func TestMergeRebaseNeverUpdatesOtherRefs(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, source, "feature.txt", "feature")
	runMergeGit(t, source, "add", ".")
	runMergeGit(t, source, "commit", "-m", "feature")
	old := mergeGitOutput(t, source, "rev-parse", "HEAD")
	runMergeGit(t, source, "branch", "retained-ref", old)
	runMergeGit(t, source, "config", "rebase.updateRefs", "true")
	writeMergeFile(t, repo, "target.txt", "target")
	runMergeGit(t, repo, "add", ".")
	runMergeGit(t, repo, "commit", "-m", "target")
	if _, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item, Rebase: true}); err != nil {
		t.Fatal(err)
	}
	if mergeGitOutput(t, repo, "rev-parse", "retained-ref") != old {
		t.Fatal("rebase.updateRefs rewrote another branch")
	}
}
func TestMergeExclusionsRemainInformational(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	file, err := os.Create(filepath.Join(source, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(gitpkg.SnapshotFileLimit + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err != nil {
		t.Fatal("exclusion blocked merge", err)
	}
	state, _ := a.ReviewRecords.LoadReview(context.Background(), item)
	if len(state.Revisions[0].Excluded) == 0 || mergeGitOutput(t, repo, "rev-parse", result.Commit+":large.bin") == "" {
		t.Fatal("excluded path was not integrated")
	}
}

func mergeWorktreePaths(t *testing.T, repo string) string {
	t.Helper()
	var paths []string
	for _, line := range strings.Split(mergeGitOutput(t, repo, "worktree", "list", "--porcelain"), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, line)
		}
	}
	return strings.Join(paths, "\n")
}

type mergeSourceMove struct {
	gitpkg.Client
	source string
}

func (g mergeSourceMove) SquashCommit(ctx context.Context, dir, tree, parent, message string) (string, error) {
	commit, err := g.Client.SquashCommit(ctx, dir, tree, parent, message)
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "git", "-C", g.source, "commit", "--allow-empty", "-m", "concurrent source commit").CombinedOutput()
	if err != nil {
		return "", errors.New(string(out))
	}
	return commit, nil
}
func TestMergeRejectsConcurrentSourceCommitWithoutReset(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, source, "feature.txt", "retained leftover\n")
	a.Git = mergeSourceMove{Client: gitpkg.New("git"), source: source}
	target := mergeGitOutput(t, repo, "rev-parse", "main")
	_, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err == nil || !strings.Contains(err.Error(), "source branch or HEAD changed") {
		t.Fatal(err)
	}
	if mergeGitOutput(t, source, "log", "-1", "--format=%s") != "concurrent source commit" || mergeGitOutput(t, repo, "rev-parse", "main") != target || readMergeFile(t, source, "feature.txt") != "retained leftover\n" {
		t.Fatal("concurrent source work reset or target moved")
	}
}

type mergeCancelled struct {
	gitpkg.Client
	cancel context.CancelFunc
}

func (g mergeCancelled) RebaseOnto(ctx context.Context, _, _ string) error {
	g.cancel()
	return ctx.Err()
}
func TestMergeCancellationRetainsCommittedLeftoversWithoutTemporaryWorktree(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, repo, "target.txt", "target")
	runMergeGit(t, repo, "add", ".")
	runMergeGit(t, repo, "commit", "-m", "advance")
	writeMergeFile(t, source, "leftover.txt", "keep")
	before := mergeGitOutput(t, repo, "rev-parse", "main")
	worktrees := mergeWorktreePaths(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Git = mergeCancelled{Client: gitpkg.New("git"), cancel: cancel}
	result, err := a.MergeWorkItem(ctx, app.MergeOptions{Selector: item})
	if err == nil || !result.SourceCommitted || result.TargetAdvanced {
		t.Fatal(result, err)
	}
	if mergeGitOutput(t, repo, "rev-parse", "main") != before || mergeWorktreePaths(t, repo) != worktrees || mergeGitOutput(t, source, "show", "HEAD:leftover.txt") != "keep" {
		t.Fatal("cancellation lost source or leaked a temporary checkout")
	}
}
func TestMergeCommitsLeftoverKindsBeforeIntegration(t *testing.T) {
	for _, kind := range []string{"untracked", "unstaged", "staged", "mixed", "deleted", "renamed", "ignored", "ignored-only"} {
		t.Run(kind, func(t *testing.T) {
			a, repo, source, item := setupMergeWorkItem(t)
			wantPath, want := "README.md", "updated\n"
			switch kind {
			case "untracked":
				wantPath = "new.txt"
				writeMergeFile(t, source, wantPath, want)
			case "unstaged":
				writeMergeFile(t, source, wantPath, want)
			case "staged":
				writeMergeFile(t, source, wantPath, want)
				runMergeGit(t, source, "add", wantPath)
			case "mixed":
				writeMergeFile(t, source, wantPath, "staged\n")
				runMergeGit(t, source, "add", wantPath)
				writeMergeFile(t, source, wantPath, want)
			case "deleted":
				if err := os.Remove(source + "/README.md"); err != nil {
					t.Fatal(err)
				}
			case "renamed":
				runMergeGit(t, source, "mv", "README.md", "renamed.md")
				wantPath, want = "renamed.md", "initial\n"
			case "ignored", "ignored-only":
				writeMergeFile(t, source, ".gitignore", "local.bin\n")
				if kind == "ignored-only" {
					runMergeGit(t, source, "add", ".gitignore")
					runMergeGit(t, source, "commit", "-m", "ignore local data")
				}
				writeMergeFile(t, source, "local.bin", "keep local\n")
				wantPath, want = "README.md", "initial\n"
			}
			result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
			if err != nil {
				t.Fatal(err)
			}
			if mergeGitOutput(t, source, "status", "--porcelain") != "" {
				t.Fatal("leftovers not committed")
			}
			if kind == "deleted" {
				if _, err := os.Stat(repo + "/README.md"); !os.IsNotExist(err) {
					t.Fatal("deletion not integrated")
				}
			} else if readMergeFile(t, repo, wantPath) != want {
				t.Fatal("wrong integrated contents", kind, result)
			}
			if strings.HasPrefix(kind, "ignored") && readMergeFile(t, source, "local.bin") != "keep local\n" {
				t.Fatal("ignored data lost")
			}
		})
	}
}
func TestMergeOverlappingDirtyTargetAfterRebaseRetainsBothCheckouts(t *testing.T) {
	a, repo, source, item := setupMergeWorkItem(t)
	writeMergeFile(t, source, "README.md", "source\n")
	writeMergeFile(t, repo, "target.txt", "target")
	runMergeGit(t, repo, "add", ".")
	runMergeGit(t, repo, "commit", "-m", "advance target")
	before := mergeGitOutput(t, repo, "rev-parse", "main")
	writeMergeFile(t, repo, "README.md", "keep target WIP\n")
	result, err := a.MergeWorkItem(context.Background(), app.MergeOptions{Selector: item})
	if err == nil || !result.Rebased || result.TargetAdvanced {
		t.Fatal(result, err)
	}
	if mergeGitOutput(t, repo, "rev-parse", "main") != before || readMergeFile(t, repo, "README.md") != "keep target WIP\n" || readMergeFile(t, source, "README.md") != "source\n" {
		t.Fatal("overlap lost work")
	}
}
