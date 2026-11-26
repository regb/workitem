package daemonapi_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolatedTests(m.Run)) }

func TestItemWorkspaceAndViewThroughAPI(t *testing.T) {
	f := apitest.New(t)
	repo := f.Repository(t, "test")
	ctx := context.Background()
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "API item", Description: "searchable description", Labels: []string{"api"}, RequestId: "create-1"}))
	if err != nil {
		t.Fatal(err)
	}
	itemID := created.Msg.Item.Id
	duplicate, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "API item", Description: "searchable description", Labels: []string{"api"}, RequestId: "create-1"}))
	if err != nil || duplicate.Msg.Item.Id != itemID {
		t.Fatalf("creation retry: %v %v", duplicate, err)
	}
	resolved, err := f.Clients.Items.ResolveItem(ctx, connect.NewRequest(&v1.ResolveItemRequest{Selector: created.Msg.Item.Slug}))
	if err != nil || resolved.Msg.ItemId != itemID {
		t.Fatalf("resolve: %v %v", resolved, err)
	}
	title := "renamed"
	updated, err := f.Clients.Items.UpdateItem(ctx, connect.NewRequest(&v1.UpdateItemRequest{ItemId: itemID, ExpectedRevision: created.Msg.Item.Revision, Title: &title}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Clients.Items.UpdateItem(ctx, connect.NewRequest(&v1.UpdateItemRequest{ItemId: itemID, ExpectedRevision: created.Msg.Item.Revision, Title: &title}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale update: %v", err)
	}
	_, err = f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: itemID, State: v1.ItemState_ITEM_STATE_ACTIVE, ExpectedRevision: updated.Msg.Item.Revision}))
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := f.Clients.Workspaces.EnsureWorkspace(ctx, connect.NewRequest(&v1.EnsureWorkspaceRequest{ItemId: itemID}))
	if err != nil {
		t.Fatal(err)
	}
	apitest.Wait(t, f.Clients, workspace.Msg.Job)
	_, err = f.Clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: itemID, State: v1.ItemState_ITEM_STATE_BACKLOG}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("state with claim: %v", err)
	}
	_, err = f.Clients.Items.AddNote(ctx, connect.NewRequest(&v1.AddNoteRequest{ItemId: itemID, Text: "remember"}))
	if err != nil {
		t.Fatal(err)
	}
	attached, err := f.Clients.Items.Attach(ctx, connect.NewRequest(&v1.AttachRequest{ItemId: itemID, Name: "evidence.txt", Content: []byte("evidence")}))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := f.Clients.Items.ReadAttachment(ctx, connect.NewRequest(&v1.ReadAttachmentRequest{ItemId: itemID, Sha256: attached.Msg.Note.Attachment.Sha256}))
	if err != nil {
		t.Fatal(err)
	}
	var content []byte
	for stream.Receive() {
		content = append(content, stream.Msg().Chunk...)
	}
	stream.Close()
	if stream.Err() != nil || string(content) != "evidence" {
		t.Fatalf("attachment: %s %v", content, stream.Err())
	}
	view, err := f.Clients.Views.GetItemView(ctx, connect.NewRequest(&v1.GetItemViewRequest{ItemId: itemID}))
	if err != nil {
		t.Fatal(err)
	}
	if view.Msg.Item.Id != itemID || view.Msg.Description != "searchable description" || view.Msg.Setup == nil {
		t.Fatalf("view: %v", view.Msg)
	}
	search, err := f.Clients.Items.SearchItems(ctx, connect.NewRequest(&v1.SearchItemsRequest{Query: "searchable"}))
	if err != nil || search.Msg.Total != 1 {
		t.Fatalf("search: %v %v", search, err)
	}
	_, err = f.Clients.Workspaces.ReleaseWorkspace(ctx, connect.NewRequest(&v1.ReleaseWorkspaceRequest{ItemId: itemID}))
	if err != nil {
		t.Fatal(err)
	}
	closed, err := f.Clients.Lifecycle.CloseItem(ctx, connect.NewRequest(&v1.CloseItemRequest{ItemId: itemID, Resolution: v1.Resolution_RESOLUTION_COMPLETED}))
	if err != nil {
		t.Fatal(err)
	}
	apitest.Wait(t, f.Clients, closed.Msg.Job)
	final, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: itemID}))
	if err != nil || final.Msg.Item.State != v1.ItemState_ITEM_STATE_CLOSED {
		t.Fatalf("close: %v %v", final, err)
	}
}
func TestConcurrentFencedUpdatesHaveOneWinner(t *testing.T) {
	f := apitest.New(t)
	repo := f.Repository(t, "test")
	created, err := f.Clients.Items.CreateItem(context.Background(), connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "race"}))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, title := range []string{"first", "second"} {
		wg.Add(1)
		go func(title string) {
			defer wg.Done()
			_, err := f.Clients.Items.UpdateItem(context.Background(), connect.NewRequest(&v1.UpdateItemRequest{ItemId: created.Msg.Item.Id, ExpectedRevision: created.Msg.Item.Revision, Title: &title}))
			errs <- err
		}(title)
	}
	wg.Wait()
	close(errs)
	success, stale := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if connect.CodeOf(err) == connect.CodeAborted {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatalf("winners=%d stale=%d", success, stale)
	}
}
func TestChangeStreamInitialAndCancellation(t *testing.T) {
	f := apitest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := f.Clients.Events.WatchChanges(ctx, connect.NewRequest(&v1.WatchChangesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || stream.Msg().Cursor == "" || stream.Msg().All {
		t.Fatalf("first event: %v %v", stream.Msg(), stream.Err())
	}
	cancel()
	stream.Receive()
	stream.Close()
	if !errors.Is(stream.Err(), context.Canceled) && connect.CodeOf(stream.Err()) != connect.CodeCanceled {
		t.Fatal(stream.Err())
	}
}
