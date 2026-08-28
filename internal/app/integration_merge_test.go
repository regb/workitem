package app_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestMergeWorkItemRebasesFastForwardsAndSyncsTargetWorktree(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	writeMergeFile(t, source, "feature.txt", "feature\n")
	runMergeGit(t, source, "add", "feature.txt")
	runMergeGit(t, source, "commit", "-m", "feature")
	writeMergeFile(t, repo, "local.txt", "base\n")
	runMergeGit(t, repo, "add", "local.txt")
	runMergeGit(t, repo, "commit", "-m", "advance target")
	writeMergeFile(t, repo, "local.txt", "keep dirty\n")

	res, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, CWD: source})
	if err != nil {
		t.Fatal(err)
	}
	if res.TargetBranch != "main" || !res.Rebased || !res.TargetAdvanced || !res.TargetSynced || res.RolledBack || res.SourceOldSHA == res.SourceNewSHA {
		t.Fatalf("result = %+v", res)
	}
	if got := strings.TrimSpace(readMergeFile(t, repo, "feature.txt")); got != "feature" {
		t.Fatalf("target content = %q", got)
	}
	if got := strings.TrimSpace(readMergeFile(t, repo, "local.txt")); got != "keep dirty" {
		t.Fatalf("non-overlapping dirty file = %q", got)
	}
	if mainSHA, sourceSHA := mergeGitOutput(t, repo, "rev-parse", "main"), mergeGitOutput(t, source, "rev-parse", "HEAD"); mainSHA != sourceSHA {
		t.Fatalf("main=%s source=%s", mainSHA, sourceSHA)
	}
	manifest, err := application.Store.LoadManifest(itemID)
	if err != nil || manifest.State != "working" || manifest.Checkout.Path == nil {
		t.Fatalf("manifest changed unexpectedly: %+v err=%v", manifest, err)
	}
	events, err := application.Store.(*store.Store).ReadEvents(itemID)
	if err != nil {
		t.Fatal(err)
	}
	types := []string{}
	for _, event := range events {
		if strings.HasPrefix(event.Type, "merge.") {
			types = append(types, event.Type)
		}
	}
	if strings.Join(types, ",") != "merge.started,merge.rebased,merge.target_advanced,merge.target_synced,merge.completed" {
		t.Fatalf("merge events = %v", types)
	}
}

func TestMergeWorkItemExplicitTargetWithoutWorktree(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	runMergeGit(t, repo, "branch", "develop", "main")
	writeMergeFile(t, source, "feature.txt", "feature\n")
	runMergeGit(t, source, "add", "feature.txt")
	runMergeGit(t, source, "commit", "-m", "feature")
	res, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, Target: "develop", CWD: source})
	if err != nil {
		t.Fatal(err)
	}
	if res.TargetBranch != "develop" || !res.TargetAdvanced || res.TargetSynced || res.TargetWorktreePath != "" {
		t.Fatalf("result = %+v", res)
	}
	if developSHA, sourceSHA := mergeGitOutput(t, repo, "rev-parse", "develop"), mergeGitOutput(t, source, "rev-parse", "HEAD"); developSHA != sourceSHA {
		t.Fatalf("develop=%s source=%s", developSHA, sourceSHA)
	}
}

func TestMergeWorkItemRejectsMissingRegisteredTargetWorktreeBeforeRebase(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	targetPath := filepath.Join(t.TempDir(), "missing-target")
	runMergeGit(t, repo, "branch", "develop", "main")
	runMergeGit(t, repo, "worktree", "add", targetPath, "develop")
	if err := os.RemoveAll(targetPath); err != nil {
		t.Fatal(err)
	}
	writeMergeFile(t, source, "feature.txt", "feature\n")
	runMergeGit(t, source, "add", "feature.txt")
	runMergeGit(t, source, "commit", "-m", "feature")
	sourceOld := mergeGitOutput(t, source, "rev-parse", "HEAD")
	_, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, Target: "develop", CWD: source})
	if err == nil || !strings.Contains(err.Error(), "unavailable worktree") || !strings.Contains(err.Error(), "git worktree prune") {
		t.Fatalf("error = %v", err)
	}
	if got := mergeGitOutput(t, source, "rev-parse", "HEAD"); got != sourceOld {
		t.Fatalf("source moved before preflight completed: %s != %s", got, sourceOld)
	}
}

func TestMergeWorkItemRejectsDirtySource(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dirty func(*testing.T, string)
	}{
		{name: "untracked", dirty: func(t *testing.T, source string) { writeMergeFile(t, source, "untracked.txt", "dirty\n") }},
		{name: "unstaged", dirty: func(t *testing.T, source string) { writeMergeFile(t, source, "README.md", "dirty\n") }},
		{name: "staged", dirty: func(t *testing.T, source string) {
			writeMergeFile(t, source, "staged.txt", "dirty\n")
			runMergeGit(t, source, "add", "staged.txt")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			application, _, source, itemID := setupMergeWorkItem(t)
			tc.dirty(t, source)
			oldSHA := mergeGitOutput(t, source, "rev-parse", "HEAD")
			_, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, Target: "main", CWD: source})
			if err == nil || !strings.Contains(err.Error(), "completely clean") {
				t.Fatalf("error = %v", err)
			}
			if got := mergeGitOutput(t, source, "rev-parse", "HEAD"); got != oldSHA {
				t.Fatalf("source moved: %s -> %s", oldSHA, got)
			}
		})
	}
}

type movedTargetGit struct{ gitpkg.Client }

func (f movedTargetGit) UpdateRef(ctx context.Context, repoRoot, ref, newSHA, oldSHA, message string) error {
	if message == "wi merge: fast-forward" {
		return errors.New("injected compare-and-swap failure")
	}
	return f.Client.UpdateRef(ctx, repoRoot, ref, newSHA, oldSHA, message)
}

func TestMergeWorkItemRestoresSourceWhenTargetMoves(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	application.Git = movedTargetGit{Client: gitpkg.New("git")}
	writeMergeFile(t, source, "feature.txt", "feature\n")
	runMergeGit(t, source, "add", "feature.txt")
	runMergeGit(t, source, "commit", "-m", "feature")
	sourceOld := mergeGitOutput(t, source, "rev-parse", "HEAD")
	targetOld := mergeGitOutput(t, repo, "rev-parse", "main")
	res, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, Target: "main", CWD: source})
	if err == nil || !strings.Contains(err.Error(), "target branch main moved") || !res.RolledBack {
		t.Fatalf("result=%+v error=%v", res, err)
	}
	if got := mergeGitOutput(t, source, "rev-parse", "HEAD"); got != sourceOld {
		t.Fatalf("source was not restored: %s != %s", got, sourceOld)
	}
	if got := mergeGitOutput(t, repo, "rev-parse", "main"); got != targetOld {
		t.Fatalf("target moved: %s != %s", got, targetOld)
	}
}

type failingSyncGit struct{ gitpkg.Client }

func (f failingSyncGit) ReadTreeMergeUpdate(context.Context, string, string, string) error {
	return errors.New("injected target sync failure")
}

func TestMergeWorkItemRollsBackTargetAndSourceWhenSyncFails(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	application.Git = failingSyncGit{Client: gitpkg.New("git")}
	writeMergeFile(t, source, "feature.txt", "feature\n")
	runMergeGit(t, source, "add", "feature.txt")
	runMergeGit(t, source, "commit", "-m", "feature")
	sourceOld := mergeGitOutput(t, source, "rev-parse", "HEAD")
	targetOld := mergeGitOutput(t, repo, "rev-parse", "main")
	res, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, Target: "main", CWD: source})
	if err == nil || !strings.Contains(err.Error(), "target_sync") || !res.RolledBack {
		t.Fatalf("result=%+v error=%v", res, err)
	}
	if got := mergeGitOutput(t, source, "rev-parse", "HEAD"); got != sourceOld {
		t.Fatalf("source was not restored: %s != %s", got, sourceOld)
	}
	if got := mergeGitOutput(t, repo, "rev-parse", "main"); got != targetOld {
		t.Fatalf("target was not restored: %s != %s", got, targetOld)
	}
}

func TestMergeWorkItemAbortsConflictingRebase(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	writeMergeFile(t, source, "README.md", "source\n")
	runMergeGit(t, source, "add", "README.md")
	runMergeGit(t, source, "commit", "-m", "source change")
	sourceOld := mergeGitOutput(t, source, "rev-parse", "HEAD")
	writeMergeFile(t, repo, "README.md", "target\n")
	runMergeGit(t, repo, "add", "README.md")
	runMergeGit(t, repo, "commit", "-m", "target change")
	targetOld := mergeGitOutput(t, repo, "rev-parse", "main")

	res, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, Target: "main", CWD: source})
	if err == nil || !strings.Contains(err.Error(), "failed and was aborted") || !res.RolledBack {
		t.Fatalf("result=%+v error=%v", res, err)
	}
	if got := mergeGitOutput(t, source, "rev-parse", "HEAD"); got != sourceOld {
		t.Fatalf("source was not restored: %s != %s", got, sourceOld)
	}
	if got := mergeGitOutput(t, repo, "rev-parse", "main"); got != targetOld {
		t.Fatalf("target moved: %s != %s", got, targetOld)
	}
	gitDir := mergeGitOutput(t, source, "rev-parse", "--git-path", "rebase-merge")
	if _, err := os.Stat(gitDir); !os.IsNotExist(err) {
		t.Fatalf("rebase remains in progress: %v", err)
	}
}

func TestMergeWorkItemRollsBackRebaseForOverlappingDirtyTarget(t *testing.T) {
	application, repo, source, itemID := setupMergeWorkItem(t)
	writeMergeFile(t, source, "README.md", "source committed\n")
	runMergeGit(t, source, "add", "README.md")
	runMergeGit(t, source, "commit", "-m", "source change")
	sourceOld := mergeGitOutput(t, source, "rev-parse", "HEAD")
	writeMergeFile(t, repo, "target.txt", "target commit\n")
	runMergeGit(t, repo, "add", "target.txt")
	runMergeGit(t, repo, "commit", "-m", "advance target")
	targetOld := mergeGitOutput(t, repo, "rev-parse", "main")
	writeMergeFile(t, repo, "README.md", "target dirty\n")

	res, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: itemID, Target: "main", CWD: source})
	if err == nil || !strings.Contains(err.Error(), "overlapping incoming update") || !res.RolledBack {
		t.Fatalf("result=%+v error=%v", res, err)
	}
	if got := mergeGitOutput(t, source, "rev-parse", "HEAD"); got != sourceOld {
		t.Fatalf("source was not rolled back: %s != %s", got, sourceOld)
	}
	if got := mergeGitOutput(t, repo, "rev-parse", "main"); got != targetOld {
		t.Fatalf("target moved: %s != %s", got, targetOld)
	}
	if got := strings.TrimSpace(readMergeFile(t, repo, "README.md")); got != "target dirty" {
		t.Fatalf("dirty target file changed: %q", got)
	}
}

func TestMergeWorkItemRecordsManagedTargetEvent(t *testing.T) {
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
	application := app.New(st, gitpkg.New("git"))
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 95), testutil.ID(t, 96), testutil.ID(t, 97), testutil.ID(t, 98)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	targetItem, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Managed Target", CWD: repo})
	if err != nil {
		t.Fatal(err)
	}
	target, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: targetItem.Manifest.ID, CWD: repo}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	sourceItem, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Managed Source", CWD: repo})
	if err != nil {
		t.Fatal(err)
	}
	source, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: sourceItem.Manifest.ID, CWD: repo}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := *source.Workspace.Checkout.Path
	writeMergeFile(t, sourcePath, "feature.txt", "feature\n")
	runMergeGit(t, sourcePath, "add", "feature.txt")
	runMergeGit(t, sourcePath, "commit", "-m", "feature")
	res, err := application.MergeWorkItem(context.Background(), app.MergeOptions{Selector: sourceItem.Manifest.ID, Target: target.Workspace.Checkout.Branch, CWD: sourcePath})
	if err != nil {
		t.Fatal(err)
	}
	if res.TargetManagedWorkItemID != targetItem.Manifest.ID || !res.TargetSynced {
		t.Fatalf("result = %+v", res)
	}
	events, err := st.ReadEvents(targetItem.Manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type == "checkout.updated_by_merge" {
			found = true
		}
	}
	if !found {
		t.Fatalf("target events = %+v", events)
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
	application := app.New(st, gitpkg.New("git"))
	application.Tmux = &fakeTmux{}
	application.Clock = fixedClock{testutil.Time()}
	ids := []string{testutil.ID(t, 91), testutil.ID(t, 92)}
	idx := 0
	application.NewID = func() (string, error) { id := ids[idx]; idx++; return id, nil }
	created, err := application.NewWorkItem(context.Background(), app.NewWorkItemOptions{Title: "Merge Feature", CWD: repo})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartWorkItem(context.Background(), app.ResolveOptions{Selector: created.Manifest.ID, CWD: repo}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	source := *started.Workspace.Checkout.Path
	runMergeGit(t, source, "config", "user.email", "test@example.com")
	runMergeGit(t, source, "config", "user.name", "Test User")
	return application, repo, source, created.Manifest.ID
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
