package daemonapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
)

func reviewWorkspace(t *testing.T) (*fixture, string, *v1.Checkout) {
	t.Helper()
	return fileWorkspace(t)
}

func TestReviewAndEditorThroughCoreAPI(t *testing.T) {
	f, item, checkout := reviewWorkspace(t)
	ctx := context.Background()
	claim := checkout.Claim
	dir := claim.Path
	p := "source with\nnewline.go"
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(p, "old\n")
	write(".gitignore", "ignored\n")
	write("ignored", "secret")
	files, err := f.clients.Workspaces.ListWorkspaceFiles(ctx, connect.NewRequest(&v1.ListWorkspaceFilesRequest{ItemId: item}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, path := range files.Msg.Paths {
		if path == p {
			found = true
		}
		if path == "ignored" {
			t.Fatal("ignored path listed")
		}
	}
	if !found {
		t.Fatalf("paths: %v", files.Msg.Paths)
	}
	source, err := f.clients.Workspaces.GetWorkspaceFile(ctx, connect.NewRequest(&v1.GetWorkspaceFileRequest{ItemId: item, Path: p, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}))
	if err != nil {
		t.Fatal(err)
	}
	save := &v1.SaveWorkspaceFileRequest{ItemId: item, Path: p, Content: "edited\n", ExpectedSha256: source.Msg.File.Sha256, ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}
	saved, err := f.clients.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(save))
	if err != nil || saved.Msg.File.Content != "edited\n" {
		t.Fatalf("save: %v %v", saved, err)
	}
	if _, err = f.clients.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(save)); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale content: %v", err)
	}
	save.ExpectedSha256 = saved.Msg.File.Sha256
	save.ExpectedClaimGeneration++
	if _, err = f.clients.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(save)); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale claim: %v", err)
	}
	save.ExpectedClaimGeneration--
	save.Path = ".git/config"
	if _, err = f.clients.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(save)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("metadata: %v", err)
	}
	comparison := v1.ReviewComparison_REVIEW_COMPARISON_ITEM
	review, err := f.clients.Reviews.GetReview(ctx, connect.NewRequest(&v1.GetReviewRequest{ItemId: item, Comparison: comparison}))
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Msg.Files) != 2 || !review.Msg.Revision.WorkingTree {
		t.Fatalf("review: %v", review.Msg)
	}
	diffReq := &v1.GetReviewFileRequest{ItemId: item, Comparison: comparison, Path: p, ExpectedVersion: review.Msg.Revision.Version}
	diff, err := f.clients.Reviews.GetReviewFile(ctx, connect.NewRequest(diffReq))
	if err != nil || diff.Msg.OldFile.Exists || diff.Msg.NewFile.Content != "edited\n" {
		t.Fatalf("diff: %v %v", diff, err)
	}
	for _, args := range [][]string{{"config", "user.name", "Review test"}, {"config", "user.email", "test@example.invalid"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("config: %v %s", err, out)
		}
	}
	committed, err := f.clients.Workspaces.CommitWorkspace(ctx, connect.NewRequest(&v1.CommitWorkspaceRequest{ItemId: item, Message: "Add reviewed files"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.clients.Reviews.GetReviewFile(ctx, connect.NewRequest(diffReq)); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale revision: %v", err)
	}
	history, err := f.clients.Reviews.ListReviewCommits(ctx, connect.NewRequest(&v1.ListReviewCommitsRequest{ItemId: item}))
	if err != nil || len(history.Msg.Commits) != 1 || history.Msg.Commits[0].Sha != committed.Msg.Commit {
		t.Fatalf("history: %v %v", history, err)
	}
	selected, err := f.clients.Reviews.GetReview(ctx, connect.NewRequest(&v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: committed.Msg.Commit}))
	if err != nil || len(selected.Msg.Files) != 2 {
		t.Fatalf("commit review: %v %v", selected, err)
	}
	for _, sha := range []string{"--all", "HEAD", "0000000000000000000000000000000000000000"} {
		if _, err = f.clients.Reviews.GetReview(ctx, connect.NewRequest(&v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_COMMIT, Commit: sha})); err == nil {
			t.Fatalf("accepted invalid commit %s", sha)
		}
	}
	if _, err = f.clients.Workspaces.ReleaseWorkspace(ctx, connect.NewRequest(&v1.ReleaseWorkspaceRequest{ItemId: item})); err != nil {
		t.Fatal(err)
	}
	bare, err := f.clients.Reviews.GetReview(ctx, connect.NewRequest(&v1.GetReviewRequest{ItemId: item}))
	if err != nil || bare.Msg.Revision.WorkingTree || len(bare.Msg.Files) != 2 {
		t.Fatalf("review without checkout: %v %v", bare, err)
	}
	bareDiff, err := f.clients.Reviews.GetReviewFile(ctx, connect.NewRequest(&v1.GetReviewFileRequest{ItemId: item, Path: p, ExpectedVersion: bare.Msg.Revision.Version}))
	if err != nil || bareDiff.Msg.NewFile.Content != "edited\n" {
		t.Fatalf("bare diff: %v %v", bareDiff, err)
	}
	if _, err = f.clients.Reviews.GetReview(ctx, connect.NewRequest(&v1.GetReviewRequest{ItemId: item, Comparison: v1.ReviewComparison_REVIEW_COMPARISON_UNCOMMITTED})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("missing checkout: %v", err)
	}
	agent, err := apitransport.NewClients(filepath.Join(f.root, "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if _, err = agent.Reviews.GetReview(ctx, connect.NewRequest(&v1.GetReviewRequest{ItemId: item})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("agent review access: %v", err)
	}
	if _, err = agent.Workspaces.SaveWorkspaceFile(ctx, connect.NewRequest(save)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("agent save access: %v", err)
	}
}
