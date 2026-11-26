package daemonapi_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestReviewDeletionReportsLeftoverSnapshotWithoutRetainingRecords(t *testing.T) {
	f, item, ws := reviewWorkspace(t)
	ctx := context.Background()
	file := filepath.Join(ws.Claim.Path, "review.txt")
	if err := os.WriteFile(file, []byte("snapshot\n"), 0600); err != nil {
		t.Fatal(err)
	}
	captured, err := f.Clients.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(&v1.CaptureReviewRevisionRequest{ItemId: item, CaptureId: "delete-test", ExpectedClaimId: ws.Claim.Id, ExpectedClaimGeneration: ws.Claim.Generation}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Clients.Workspaces.ReleaseWorkspace(ctx, connect.NewRequest(&v1.ReleaseWorkspaceRequest{ItemId: item})); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: item, State: v1.ItemState_ITEM_STATE_CLOSED, Resolution: v1.Resolution_RESOLUTION_COMPLETED})); err != nil {
		t.Fatal(err)
	}
	m, err := f.App().Store.LoadManifest(item)
	if err != nil {
		t.Fatal(err)
	}
	root := m.Repository.OperationalRoot()
	gitdir := integrationGit(t, root, "rev-parse", "--absolute-git-dir")
	locked := filepath.Join(gitdir, filepath.FromSlash(captured.Msg.Revision.Ref)+".lock")
	if err := os.WriteFile(locked, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(locked)
	_, err = f.Clients.Items.DeleteItem(ctx, connect.NewRequest(&v1.DeleteItemRequest{ItemId: item, Confirm: true}))
	if err == nil {
		t.Fatal("leftover snapshot was not reported")
	}
	var partial *v1.PartialOutcome
	if ce, ok := err.(*connect.Error); ok {
		for _, d := range ce.Details() {
			v, e := d.Value()
			if e == nil {
				partial, _ = v.(*v1.PartialOutcome)
				if partial != nil {
					break
				}
			}
		}
	}
	if partial == nil || len(partial.Completed) == 0 {
		t.Fatal("deletion error did not retain successful durable outcome", err)
	}
	if _, found, err := f.Core().Database().ReadReviewRecord(item); err != nil || found {
		t.Fatal("ref deletion failure retained review records", err)
	}
	if _, err := f.App().Store.LoadManifest(item); err == nil {
		t.Fatal("ref deletion failure retained item")
	}
	if ref := integrationGit(t, root, "rev-parse", captured.Msg.Revision.Ref); ref != captured.Msg.Revision.Commit {
		t.Fatal("test did not leave the expected harmless snapshot ref")
	}
}
