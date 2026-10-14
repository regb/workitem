package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/testutil"
)

type syncFixture struct {
	t                           *testing.T
	server                      *Server
	root, origin, store, socket string
}

func syncGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	f := &syncFixture{t: t, root: t.TempDir(), origin: t.TempDir()}
	syncGit(t, f.origin, "init", "-b", "main")
	syncGit(t, f.origin, "config", "user.name", "Test")
	syncGit(t, f.origin, "config", "user.email", "test@example.com")
	f.commit(f.origin, "document", "base\n", "base")
	var err error
	f.store, err = model.OwnedRepositoryGitDir(f.root, importTestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := gitpkg.New("git").ImportBare(context.Background(), model.BareImportOptions{Source: f.origin, Destination: f.store, OriginURL: f.origin, IntegrationBranch: "main", UpstreamBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	syncGit(t, f.store, "config", "user.name", "Test")
	syncGit(t, f.store, "config", "user.email", "test@example.com")
	f.socket, err = SocketPath(testutil.ShortTempDir(t), f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.server, err = NewServer(f.root, f.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.server.Close() })
	command := repositoryTestCommand(importTestID, "project")
	if _, err := f.server.database.RegisterRepository(command); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *syncFixture) commit(dir, file, content, message string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0600); err != nil {
		f.t.Fatal(err)
	}
	syncGit(f.t, dir, "add", "--", file)
	syncGit(f.t, dir, "commit", "-m", message)
	return syncGit(f.t, dir, "rev-parse", "HEAD")
}
func (f *syncFixture) localCommit(file, content, message string) string {
	f.t.Helper()
	path := filepath.Join(f.t.TempDir(), "checkout")
	old := syncGit(f.t, f.store, "rev-parse", "main")
	syncGit(f.t, f.store, "worktree", "add", "--detach", path, old)
	sha := f.commit(path, file, content, message)
	syncGit(f.t, f.store, "update-ref", "refs/heads/main", sha, old)
	syncGit(f.t, f.store, "worktree", "remove", path)
	return sha
}
func (f *syncFixture) status(fetch bool) RepositoryStatusResult {
	f.t.Helper()
	result, err := f.server.repositoryStatus(context.Background(), RepositoryStatusRequest{Selector: "project", Fetch: fetch})
	if err != nil {
		f.t.Fatal(err)
	}
	return result
}
func (f *syncFixture) sync(mode string) (RepositorySyncResult, error) {
	return f.server.repositorySync(context.Background(), RepositorySyncRequest{Selector: "project", Mode: mode})
}

func TestRepositorySyncFastForwardDoesNotTouchOriginalCheckoutOrItems(t *testing.T) {
	f := newSyncFixture(t)
	old := f.status(false)
	if old.State != "unfetched" {
		t.Fatalf("status=%+v", old)
	}
	syncGit(t, f.store, "branch", "wi/item", old.LocalSHA)
	remote := f.commit(f.origin, "document", "remote\n", "remote change")
	if err := os.WriteFile(filepath.Join(f.origin, "untracked"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.origin, "document"), []byte("dirty original"), 0600); err != nil {
		t.Fatal(err)
	}
	before := syncGit(t, f.origin, "status", "--porcelain")
	result, err := f.sync("ff")
	if err != nil || !result.Changed || result.State != "equal" || result.LocalSHA != remote || result.FetchedAt == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := syncGit(t, f.store, "rev-parse", "wi/item"); got != old.LocalSHA {
		t.Fatal("item branch changed")
	}
	if got := syncGit(t, f.store, "rev-parse", result.RecoveryRef); got != old.LocalSHA {
		t.Fatal("old integration tip not protected")
	}
	if syncGit(t, f.origin, "status", "--porcelain") != before || syncGit(t, f.origin, "rev-parse", "HEAD") != remote {
		t.Fatal("original checkout modified")
	}
	if content, _ := os.ReadFile(filepath.Join(f.origin, "document")); string(content) != "dirty original" {
		t.Fatal("original dirty content modified")
	}
	second, err := f.sync("ff")
	if err != nil || second.Changed {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestRepositorySyncEquivalentDivergenceRequiresDecisionAndResetRetainsHistory(t *testing.T) {
	f := newSyncFixture(t)
	local := f.localCommit("document", "identical\n", "local equivalent")
	remote := f.commit(f.origin, "document", "identical\n", "remote equivalent")
	syncGit(t, f.store, "branch", "wi/item", local)
	status := f.status(true)
	if status.State != "diverged" || !status.EqualTrees || status.Ahead != 1 || status.Behind != 1 {
		t.Fatalf("status=%+v", status)
	}
	if _, err := f.sync("ff"); err == nil || !strings.Contains(err.Error(), "trees are identical") {
		t.Fatalf("err=%v", err)
	}
	if _, err := f.sync("reset"); err == nil {
		t.Fatal("unconfirmed reset accepted")
	}
	request := RepositorySyncRequest{Selector: "project", Mode: "reset", ConfirmReset: true, ExpectedLocalSHA: local, ExpectedUpstreamSHA: remote}
	request.ExpectedLocalSHA = status.UpstreamSHA
	if _, err := f.server.repositorySync(context.Background(), request); err == nil {
		t.Fatal("stale reset confirmation accepted")
	}
	request.ExpectedLocalSHA = local
	result, err := f.server.repositorySync(context.Background(), request)
	if err != nil || !result.Changed || result.LocalSHA != remote {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if syncGit(t, f.store, "rev-parse", result.RecoveryRef) != local || syncGit(t, f.store, "rev-parse", "wi/item") != local {
		t.Fatal("reset lost local history or changed item")
	}
	if syncGit(t, f.origin, "rev-parse", "HEAD") != remote {
		t.Fatal("remote modified")
	}
}

func TestRepositorySyncMergePreservesBothHistories(t *testing.T) {
	f := newSyncFixture(t)
	local := f.localCommit("local-file", "local\n", "local change")
	remote := f.commit(f.origin, "remote-file", "remote\n", "remote change")
	if _, err := f.sync("ff"); err == nil {
		t.Fatal("divergence accepted")
	}
	result, err := f.sync("merge")
	if err != nil || !result.Changed || result.State != "ahead" || result.Pending != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, sha := range []string{local, remote} {
		syncGit(t, f.store, "merge-base", "--is-ancestor", sha, "main")
	}
	if syncGit(t, f.store, "rev-parse", result.RecoveryRef) != local {
		t.Fatal("recovery tip missing")
	}
	if syncGit(t, f.origin, "rev-parse", "HEAD") != remote {
		t.Fatal("merge pushed remotely")
	}
}

func TestRepositorySyncConflictSurvivesRestartAndCanContinue(t *testing.T) {
	f := newSyncFixture(t)
	local := f.localCommit("document", "local\n", "local change")
	remote := f.commit(f.origin, "document", "remote\n", "remote change")
	if _, err := f.sync("merge"); err == nil || !strings.Contains(err.Error(), "reconciliation retained") {
		t.Fatalf("err=%v", err)
	}
	status := f.status(false)
	if status.Pending == nil || status.LocalSHA != local || status.ReconciliationPath == "" {
		t.Fatalf("status=%+v", status)
	}
	f.server.Close()
	var err error
	f.server, err = NewServer(f.root, f.socket)
	if err != nil {
		t.Fatal(err)
	}
	reopened := f.status(false)
	if reopened.ReconciliationPath != status.ReconciliationPath {
		t.Fatal("pending path changed")
	}
	// Native resolution remains operator-owned until explicit continuation.
	if err := os.WriteFile(filepath.Join(status.ReconciliationPath, "document"), []byte("both\n"), 0600); err != nil {
		t.Fatal(err)
	}
	syncGit(t, status.ReconciliationPath, "add", "document")
	result, err := f.sync("continue")
	if err != nil || result.Pending != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, sha := range []string{local, remote} {
		syncGit(t, f.store, "merge-base", "--is-ancestor", sha, "main")
	}
	if _, err := os.Stat(status.ReconciliationPath); !os.IsNotExist(err) {
		t.Fatal("completed reconciliation checkout retained")
	}
}

func TestRepositorySyncAbortRetainsUntrackedResolutionFiles(t *testing.T) {
	f := newSyncFixture(t)
	local := f.localCommit("document", "local\n", "local")
	f.commit(f.origin, "document", "remote\n", "remote")
	if _, err := f.sync("merge"); err == nil {
		t.Fatal("conflict missing")
	}
	status := f.status(false)
	extra := filepath.Join(status.ReconciliationPath, "untracked-notes")
	if err := os.WriteFile(extra, []byte("operator notes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sync("abort"); err == nil {
		t.Fatal("abort discarded untracked notes")
	}
	if data, err := os.ReadFile(extra); err != nil || string(data) != "operator notes" {
		t.Fatal("notes lost")
	}
	if syncGit(t, f.store, "rev-parse", "main") != local || f.status(false).Pending == nil {
		t.Fatal("abort advanced integration or lost pending claim")
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	result, err := f.sync("abort")
	if err != nil || result.Pending != nil || result.LocalSHA != local {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRepositorySyncRecoversInterruptedRefUpdate(t *testing.T) {
	f := newSyncFixture(t)
	old := f.status(false).LocalSHA
	remote := f.commit(f.origin, "remote", "remote\n", "remote")
	f.status(true)
	id, _ := model.NewID()
	plan := RepositorySyncPlan{ID: id, Mode: "reset", Phase: "prepared", OldSHA: old, UpstreamSHA: remote, NewSHA: remote, RecoveryRef: "refs/wi/recovery/repository-sync/" + id}
	if err := f.server.saveSyncPlan(importTestID, &plan); err != nil {
		t.Fatal(err)
	}
	// Simulate the CAS succeeding before phase acknowledgement persisted.
	syncGit(t, f.store, "update-ref", "refs/heads/main", remote, old)
	result, err := f.sync("continue")
	if err != nil || result.Pending != nil || syncGit(t, f.store, "rev-parse", plan.RecoveryRef) != old {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRepositorySyncRejectsActiveGitOperationsAndCheckedOutIntegration(t *testing.T) {
	f := newSyncFixture(t)
	old := f.status(false).LocalSHA
	f.commit(f.origin, "remote", "remote\n", "remote")
	path := filepath.Join(t.TempDir(), "item")
	syncGit(t, f.store, "worktree", "add", "-b", "wi/item", path, old)
	marker := syncGit(t, path, "rev-parse", "--git-path", "CHERRY_PICK_HEAD")
	if !filepath.IsAbs(marker) {
		marker = filepath.Join(path, marker)
	}
	if err := os.WriteFile(marker, []byte(old+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sync("ff"); err == nil {
		t.Fatal("active Git operation ignored")
	}
	if syncGit(t, f.store, "rev-parse", "main") != old {
		t.Fatal("integration changed")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	syncGit(t, path, "switch", "main")
	if _, err := f.sync("ff"); err == nil || !strings.Contains(err.Error(), "checked out") {
		t.Fatalf("err=%v", err)
	}
}

func TestRepositorySyncConcurrentRequestsRefuseActiveIntegration(t *testing.T) {
	f := newSyncFixture(t)
	remote := f.commit(f.origin, "remote", "remote\n", "remote")
	var wg sync.WaitGroup
	errors := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.sync("ff"); errors <- err }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil && !strings.Contains(err.Error(), "integration is active") {
			t.Fatal(err)
		}
	}
	if f.status(false).LocalSHA != remote {
		t.Fatal("wrong integration tip")
	}
}

func TestRepositoryUpstreamDurableCASAndValidation(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	previous := f.status(false).Repository.Upstream
	target, err := model.ParseRepositoryUpstream(f.origin, "release/stable")
	if err != nil {
		t.Fatal(err)
	}
	command := SetRepositoryUpstreamCommand{ID: "upstream-1", ProtocolVersion: ProtocolVersion, RepositoryID: importTestID, PreviousUpstream: previous, Upstream: target, CreatedAt: time.Now().UTC()}
	first, err := f.server.setRepositoryUpstream(ctx, command)
	if err != nil || !first.Changed {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := f.server.setRepositoryUpstream(ctx, command)
	if err != nil || !second.Duplicate {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	stale := command
	stale.ID = "stale"
	stale.Upstream = nil
	if _, err := f.server.setRepositoryUpstream(ctx, stale); err == nil {
		t.Fatal("stale change accepted")
	}
	noop := command
	noop.ID = "noop"
	noop.PreviousUpstream = target
	if result, err := f.server.setRepositoryUpstream(ctx, noop); err != nil || result.Changed {
		t.Fatalf("noop=%+v err=%v", result, err)
	}
	malformed := command
	malformed.ID = "bad"
	malformed.Upstream = &model.RepositoryUpstream{URL: "--upload-pack", Branch: "main"}
	if _, err := f.server.setRepositoryUpstream(ctx, malformed); err == nil {
		t.Fatal("invalid remote accepted")
	}
	f.server.Close()
	f.server, err = NewServer(f.root, f.socket)
	if err != nil {
		t.Fatal(err)
	}
	status := f.status(false)
	if status.Repository.Upstream == nil || status.Repository.Upstream.Branch != "release/stable" {
		t.Fatalf("status=%+v", status)
	}
	raw, err := f.server.database.ListProjection(RepositoryProjection)
	if err != nil || strings.Contains(string(raw[importTestID]), "upstream") {
		t.Fatalf("duplicated Git configuration: %s %v", raw[importTestID], err)
	}
	pending, _ := json.Marshal(RepositorySyncPlan{ID: importTestID})
	if err := f.server.database.UpdateProjections([]ProjectionUpdate{{Projection: repositorySyncProjection, Key: importTestID, Value: pending}}); err != nil {
		t.Fatal(err)
	}
	clear := command
	clear.ID = "clear"
	clear.PreviousUpstream = target
	clear.Upstream = nil
	if _, err := f.server.setRepositoryUpstream(ctx, clear); err == nil {
		t.Fatal("pending upstream cleared")
	}
}
