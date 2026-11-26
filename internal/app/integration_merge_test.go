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
