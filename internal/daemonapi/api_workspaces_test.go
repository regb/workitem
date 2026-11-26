package daemonapi_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
)

// workspacesCheckout makes a new active item with a checkout, without
// running its setup, in a new fixture's repository.
func workspacesCheckout(t *testing.T) (*apitest.Fixture, string, *v1.Checkout) {
	t.Helper()
	f := apitest.New(t)
	item := newItem(t, f, f.Repository(t, "test"), "Workspace")
	call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: item.Id, State: v1.ItemState_ITEM_STATE_ACTIVE})
	ensured := call(t, f.Clients.Workspaces.EnsureWorkspace, &v1.EnsureWorkspaceRequest{ItemId: item.Id, SkipSetup: true})
	return f, item.Id, ensured.Checkout
}

// workspacesWrite writes a file in a checkout.
func workspacesWrite(t *testing.T, checkout *v1.Checkout, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(checkout.Claim.Path, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// A checkout is claimed, set up, committed, released and claimed again, and
// its state is reported at each step.
func TestWorkspacesClaimSetupCommitAndRelease(t *testing.T) {
	t.Parallel()
	f, item, checkout := workspacesCheckout(t)
	claim := checkout.Claim
	if claim == nil || claim.Path == "" || checkout.Branch == "" {
		t.Fatalf("ensured checkout: %v", checkout)
	}
	t.Run("ensure is idempotent", func(t *testing.T) {
		again := call(t, f.Clients.Workspaces.EnsureWorkspace, &v1.EnsureWorkspaceRequest{ItemId: item, SkipSetup: true})
		if again.Checkout.Claim.Id != claim.Id || again.Checkout.Claim.Generation != claim.Generation {
			t.Fatalf("a second ensure replaced the claim: %v -> %v", claim, again.Checkout.Claim)
		}
	})
	t.Run("a clean checkout on its branch", func(t *testing.T) {
		got := call(t, f.Clients.Workspaces.GetWorkspace, &v1.GetWorkspaceRequest{ItemId: item})
		if got.Status != v1.WorktreeStatus_WORKTREE_STATUS_CLEAN || got.Git.GetCurrentBranch() != checkout.Branch || got.Git.GetBranchMismatch() || got.Git.GetDirty() || got.Checkout.Claim.Id != claim.Id {
			t.Fatalf("workspace: %v", got)
		}
	})
	t.Run("setup runs for the current claim", func(t *testing.T) {
		apitest.Wait(t, f.Clients, call(t, f.Clients.Workspaces.SetupWorkspace, &v1.SetupWorkspaceRequest{ItemId: item}).Job)
		got := call(t, f.Clients.Workspaces.GetWorkspace, &v1.GetWorkspaceRequest{ItemId: item})
		if got.Setup.GetState() != v1.SetupState_SETUP_STATE_SUCCEEDED || got.Setup.GetClaimId() != claim.Id {
			t.Fatalf("setup: %v", got.Setup)
		}
	})
	t.Run("changes block release until committed", func(t *testing.T) {
		workspacesWrite(t, checkout, "work.txt", "work\n")
		got := call(t, f.Clients.Workspaces.GetWorkspace, &v1.GetWorkspaceRequest{ItemId: item})
		if got.Status != v1.WorktreeStatus_WORKTREE_STATUS_CHANGED || !got.Git.GetDirty() {
			t.Fatalf("changed workspace: %v", got)
		}
		changes := call(t, f.Clients.Workspaces.GetUncommittedChanges, &v1.GetUncommittedChangesRequest{ItemId: item})
		if !slices.Contains(changes.Files, "work.txt") {
			t.Fatalf("uncommitted changes: %v", changes)
		}
		refused(t, connect.CodeFailedPrecondition, f.Clients.Workspaces.ReleaseWorkspace, &v1.ReleaseWorkspaceRequest{ItemId: item})
		committed := call(t, f.Clients.Workspaces.CommitWorkspace, &v1.CommitWorkspaceRequest{ItemId: item, Message: "Add work"})
		if committed.Commit == "" || git(t, claim.Path, "rev-parse", "HEAD") != committed.Commit || git(t, claim.Path, "log", "-1", "--format=%s") != "Add work" {
			t.Fatalf("commit: %v", committed)
		}
		if changes := call(t, f.Clients.Workspaces.GetUncommittedChanges, &v1.GetUncommittedChangesRequest{ItemId: item}); len(changes.Files) != 0 {
			t.Fatalf("changes after commit: %v", changes)
		}
		if again := call(t, f.Clients.Workspaces.CommitWorkspace, &v1.CommitWorkspaceRequest{ItemId: item}); again.Commit != "" {
			t.Fatalf("committed nothing: %v", again)
		}
	})
	t.Run("release keeps the branch for the next claim", func(t *testing.T) {
		released := call(t, f.Clients.Workspaces.ReleaseWorkspace, &v1.ReleaseWorkspaceRequest{ItemId: item})
		if released.Item.Checkout.GetClaim() != nil {
			t.Fatalf("released item keeps a claim: %v", released.Item)
		}
		if got := call(t, f.Clients.Workspaces.GetWorkspace, &v1.GetWorkspaceRequest{ItemId: item}); got.Status != v1.WorktreeStatus_WORKTREE_STATUS_ABSENT {
			t.Fatalf("released workspace: %v", got)
		}
		again := call(t, f.Clients.Workspaces.EnsureWorkspace, &v1.EnsureWorkspaceRequest{ItemId: item, SkipSetup: true})
		if again.Checkout.Claim.Id == claim.Id {
			t.Fatalf("a new claim reused the released one: %v", again.Checkout.Claim)
		}
		if data, err := os.ReadFile(filepath.Join(again.Checkout.Claim.Path, "work.txt")); err != nil || string(data) != "work\n" {
			t.Fatalf("the branch's commit is lost: %q %v", data, err)
		}
	})
}

// The checkout's files are listed, searched, read and saved under the claim
// and content fences.
func TestWorkspacesFiles(t *testing.T) {
	t.Parallel()
	f, item, checkout := workspacesCheckout(t)
	claim := checkout.Claim
	odd := "source with\nnewline.go"
	workspacesWrite(t, checkout, odd, "old\n")
	workspacesWrite(t, checkout, ".gitignore", "ignored\n")
	workspacesWrite(t, checkout, "ignored", "secret")
	t.Run("list and search skip ignored files", func(t *testing.T) {
		files := call(t, f.Clients.Workspaces.ListWorkspaceFiles, &v1.ListWorkspaceFilesRequest{ItemId: item})
		if !slices.Contains(files.Paths, odd) || slices.Contains(files.Paths, "ignored") || files.ClaimId != claim.Id || files.ClaimGeneration != claim.Generation {
			t.Fatalf("files: %v", files)
		}
		found := call(t, f.Clients.Workspaces.SearchWorkspaceFiles, &v1.SearchWorkspaceFilesRequest{ItemId: item, Query: "newline", Limit: 5})
		if len(found.Paths) == 0 || found.Paths[0] != odd {
			t.Fatalf("search: %v", found)
		}
		if ignored := call(t, f.Clients.Workspaces.SearchWorkspaceFiles, &v1.SearchWorkspaceFilesRequest{ItemId: item, Query: "ignored"}); slices.Contains(ignored.Paths, "ignored") {
			t.Fatalf("search found an ignored file: %v", ignored)
		}
	})
	t.Run("reads and saves are fenced", func(t *testing.T) {
		read := &v1.GetWorkspaceFileRequest{ItemId: item, Path: odd, ExpectedClaimId: claim.Id}
		refused(t, connect.CodeInvalidArgument, f.Clients.Workspaces.GetWorkspaceFile, read)
		read.ExpectedClaimGeneration = claim.Generation
		source := call(t, f.Clients.Workspaces.GetWorkspaceFile, read)
		if !source.File.Exists || source.File.Content != "old\n" {
			t.Fatalf("read: %v", source)
		}
		if missing := call(t, f.Clients.Workspaces.GetWorkspaceFile, &v1.GetWorkspaceFileRequest{ItemId: item, Path: "missing.txt", ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}); missing.File.Exists {
			t.Fatalf("missing file: %v", missing)
		}
		save := &v1.SaveWorkspaceFileRequest{ItemId: item, Path: odd, Content: "edited\n", ExpectedSha256: source.File.Sha256, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}
		saved := call(t, f.Clients.Workspaces.SaveWorkspaceFile, save)
		if saved.File.Content != "edited\n" {
			t.Fatalf("save: %v", saved)
		}
		refused(t, connect.CodeAborted, f.Clients.Workspaces.SaveWorkspaceFile, save)
		save.ExpectedSha256 = saved.File.Sha256
		save.ExpectedClaimGeneration++
		refused(t, connect.CodeAborted, f.Clients.Workspaces.SaveWorkspaceFile, save)
		save.ExpectedClaimGeneration--
		save.Path = ".git/config"
		refused(t, connect.CodeInvalidArgument, f.Clients.Workspaces.SaveWorkspaceFile, save)
		save.Path = odd
		save.ExpectedSha256 = ""
		refused(t, connect.CodeInvalidArgument, f.Clients.Workspaces.SaveWorkspaceFile, save)
	})
	t.Run("concurrent saves have one winner", func(t *testing.T) {
		workspacesWrite(t, checkout, "concurrent.txt", "original")
		source := call(t, f.Clients.Workspaces.GetWorkspaceFile, &v1.GetWorkspaceFileRequest{ItemId: item, Path: "concurrent.txt", ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation})
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, content := range []string{"first", "second"} {
			go func() {
				<-start
				_, err := f.Clients.Workspaces.SaveWorkspaceFile(t.Context(), connect.NewRequest(&v1.SaveWorkspaceFileRequest{ItemId: item, Path: "concurrent.txt", Content: content, ExpectedSha256: source.File.Sha256, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}))
				results <- err
			}()
		}
		close(start)
		var successes, conflicts int
		for range 2 {
			switch err := <-results; {
			case err == nil:
				successes++
			case connect.CodeOf(err) == connect.CodeAborted:
				conflicts++
			default:
				t.Fatal(err)
			}
		}
		written, err := os.ReadFile(filepath.Join(claim.Path, "concurrent.txt"))
		if successes != 1 || conflicts != 1 || err != nil || (string(written) != "first" && string(written) != "second") {
			t.Fatalf("saves: %d successes, %d conflicts, file %q %v", successes, conflicts, written, err)
		}
	})
	t.Run("documents are regular PDFs under the claim", func(t *testing.T) {
		data := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 2<<20)...)
		if err := os.WriteFile(filepath.Join(claim.Path, "report.pdf"), data, 0600); err != nil {
			t.Fatal(err)
		}
		workspacesWrite(t, checkout, "fake.pdf", "<html>not PDF</html>")
		request := &v1.GetWorkspaceDocumentRequest{ItemId: item, Path: "report.pdf", ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}
		document := call(t, f.Clients.Workspaces.GetWorkspaceDocument, request)
		if !bytes.Equal(document.Content, data) || document.MediaType != "application/pdf" || len(document.Sha256) != 64 {
			t.Fatalf("PDF: %s %d bytes", document.MediaType, len(document.Content))
		}
		request.ExpectedClaimGeneration++
		refused(t, connect.CodeAborted, f.Clients.Workspaces.GetWorkspaceDocument, request)
		request.ExpectedClaimGeneration = claim.Generation
		request.ExpectedClaimId = ""
		if _, err := f.Clients.Workspaces.GetWorkspaceDocument(t.Context(), connect.NewRequest(request)); err == nil {
			t.Fatal("missing claim fence accepted")
		}
		request.ExpectedClaimId = claim.Id
		for _, unsafe := range []string{"../report.pdf", ".git/secret.pdf", "/tmp/report.pdf"} {
			request.Path = unsafe
			if _, err := f.Clients.Workspaces.GetWorkspaceDocument(t.Context(), connect.NewRequest(request)); err == nil {
				t.Fatalf("unsafe path accepted: %s", unsafe)
			}
		}
		request.Path = "missing.pdf"
		refused(t, connect.CodeNotFound, f.Clients.Workspaces.GetWorkspaceDocument, request)
		request.Path = "fake.pdf"
		refused(t, connect.CodeInvalidArgument, f.Clients.Workspaces.GetWorkspaceDocument, request)
	})
	// Last: it leaves the checkout on another branch.
	t.Run("saves refuse a checkout off its branch", func(t *testing.T) {
		source := call(t, f.Clients.Workspaces.GetWorkspaceFile, &v1.GetWorkspaceFileRequest{ItemId: item, Path: "concurrent.txt", ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation})
		git(t, claim.Path, "switch", "-c", "unexpected")
		if got := call(t, f.Clients.Workspaces.GetWorkspace, &v1.GetWorkspaceRequest{ItemId: item}); !got.Git.GetBranchMismatch() || got.Git.GetExpectedBranch() != checkout.Branch {
			t.Fatalf("branch mismatch not reported: %v", got.Git)
		}
		refused(t, connect.CodeFailedPrecondition, f.Clients.Workspaces.SaveWorkspaceFile, &v1.SaveWorkspaceFileRequest{ItemId: item, Path: "concurrent.txt", Content: "not allowed", ExpectedSha256: source.File.Sha256, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation})
	})
}
