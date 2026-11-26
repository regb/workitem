package daemonapi_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestCheckpointAPIKeepsImmutableHistoryAcrossRelease(t *testing.T) {
	f, item, checkout := reviewWorkspace(t)
	ctx := context.Background()
	claim := checkout.Claim
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(claim.Path, "example.go"), []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("first\n")
	request := &v1.CaptureReviewRevisionRequest{ItemId: item, CaptureId: "capture-one", ExpectedClaimId: claim.Id, ExpectedClaimGeneration: claim.Generation}
	one, err := f.Clients.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	if one.Msg.Revision.Id != "r1" || one.Msg.Version == 0 {
		t.Fatal(one.Msg)
	}
	replay, err := f.Clients.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(request))
	if err != nil || replay.Msg.Revision.Commit != one.Msg.Revision.Commit || !replay.Msg.Reused {
		t.Fatalf("replay: %v %v", replay, err)
	}
	request.CaptureId = "capture-same-tree"
	same, err := f.Clients.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(request))
	if err != nil || same.Msg.Revision.Id != "r2" || same.Msg.Reused {
		t.Fatalf("dedupe: %v %v", same, err)
	}
	write("second\n")
	request.CaptureId = "capture-two"
	two, err := f.Clients.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	if two.Msg.Revision.Id != "r3" || two.Msg.Revision.Commit == one.Msg.Revision.Commit {
		t.Fatal(two.Msg)
	}
	comparison := &v1.GetRevisionReviewRequest{ItemId: item, RevisionId: "r3", FromRevisionId: "r1"}
	diff, err := f.Clients.Reviews.GetRevisionReview(ctx, connect.NewRequest(comparison))
	if err != nil {
		t.Fatal(err)
	}
	fileReq := &v1.GetRevisionReviewFileRequest{Comparison: comparison, Path: "example.go", ExpectedVersion: diff.Msg.Review.Revision.Version}
	file, err := f.Clients.Reviews.GetRevisionReviewFile(ctx, connect.NewRequest(fileReq))
	if err != nil || file.Msg.File.OldFile.Content != "first\n" || file.Msg.File.NewFile.Content != "second\n" {
		t.Fatalf("immutable comparison: %v %v", file, err)
	}
	write("later unsaved edits\n")
	file, err = f.Clients.Reviews.GetRevisionReviewFile(ctx, connect.NewRequest(fileReq))
	if err != nil || file.Msg.File.NewFile.Content != "second\n" {
		t.Fatalf("live files leaked into checkpoint: %v %v", file, err)
	}
	request.CaptureId = "stale"
	request.ExpectedClaimGeneration++
	if _, err := f.Clients.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(request)); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("claim fence: %v", err)
	}
	if err := os.Remove(filepath.Join(claim.Path, "example.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Clients.Workspaces.ReleaseWorkspace(ctx, connect.NewRequest(&v1.ReleaseWorkspaceRequest{ItemId: item})); err != nil {
		t.Fatal(err)
	}
	file, err = f.Clients.Reviews.GetRevisionReviewFile(ctx, connect.NewRequest(fileReq))
	if err != nil || file.Msg.File.NewFile.Content != "second\n" {
		t.Fatalf("lost history on release: %v %v", file, err)
	}
	state, err := f.Clients.Reviews.ListReviewRevisions(ctx, connect.NewRequest(&v1.ListReviewRevisionsRequest{ItemId: item}))
	if err != nil || len(state.Msg.Revisions) != 3 {
		t.Fatalf("state: %v %v", state, err)
	}
	m, err := f.App().Store.LoadManifest(item)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", m.Repository.OperationalRoot(), "gc", "--prune=now")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gc: %v %s", err, out)
	}
	if _, err := f.Clients.Reviews.GetRevisionReviewFile(ctx, connect.NewRequest(fileReq)); err != nil {
		t.Fatalf("GC lost snapshot: %v", err)
	}
	if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: item, State: v1.ItemState_ITEM_STATE_CLOSED, Resolution: v1.Resolution_RESOLUTION_COMPLETED})); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Clients.Items.DeleteItem(ctx, connect.NewRequest(&v1.DeleteItemRequest{ItemId: item, Confirm: true})); err != nil {
		t.Fatal(err)
	}
	if _, found, err := f.Core().Database().ReadReviewRecord(item); err != nil || found {
		t.Fatal("item deletion retained coordinator review content", err)
	}
	refs, err := exec.Command("git", "-C", m.Repository.OperationalRoot(), "for-each-ref", "--format=%(refname)", "refs/wi/review/"+item+"/").CombinedOutput()
	if err != nil || len(refs) != 0 {
		t.Fatalf("explicit deletion retained review refs: %s %v", refs, err)
	}
}
