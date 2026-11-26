package daemonapi_test

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// An item's durable record through the item service: creation, names,
// fenced updates, its log, attention and blockers, and deletion.
func TestItemsRecord(t *testing.T) {
	t.Parallel()
	f := apitest.New(t)
	repo := f.Repository(t, "test")
	create := &v1.CreateItemRequest{RepositoryId: repo, Title: "Auth refactor", Description: "searchable description", Labels: []string{"api"}, RequestId: "create-1"}
	item := call(t, f.Clients.Items.CreateItem, create).Item

	t.Run("a retried creation returns the same item", func(t *testing.T) {
		if again := call(t, f.Clients.Items.CreateItem, create).Item; again.Id != item.Id {
			t.Fatalf("retry created %s, want %s", again.Id, item.Id)
		}
		if listed := call(t, f.Clients.Items.ListItems, &v1.ListItemsRequest{}).Items; len(listed) != 1 || listed[0].Id != item.Id {
			t.Fatalf("list after retry: %v", listed)
		}
	})

	// Every RPC takes an item by its whole ID. A slug, title or ID prefix is
	// refused rather than matched, so a stale or ambiguous name can never
	// act on another item; ResolveItem alone turns names into IDs.
	t.Run("items are named only by their ID", func(t *testing.T) {
		for _, name := range []string{item.Slug, item.Id[:12], item.Title} {
			refused(t, connect.CodeInvalidArgument, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: name})
			refused(t, connect.CodeInvalidArgument, f.Clients.Items.AddNote, &v1.AddNoteRequest{ItemId: name, Text: "x"})
		}
		for _, selector := range []string{item.Slug, item.Id[:12], item.Id} {
			if got := call(t, f.Clients.Items.ResolveItem, &v1.ResolveItemRequest{Selector: selector}).ItemId; got != item.Id {
				t.Errorf("ResolveItem(%q) = %s", selector, got)
			}
		}
		refused(t, connect.CodeNotFound, f.Clients.Items.ResolveItem, &v1.ResolveItemRequest{Selector: "no-such-item"})
	})

	t.Run("updates are fenced by revision", func(t *testing.T) {
		current := call(t, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: item.Id}).Item
		updated := call(t, f.Clients.Items.UpdateItem, &v1.UpdateItemRequest{ItemId: item.Id, ExpectedRevision: current.Revision, Title: proto.String("Auth refactor v2"), AddLabels: []string{"backend"}, RemoveLabels: []string{"api"}, Priority: proto.Int32(2)}).Item
		if updated.Title != "Auth refactor v2" || !slices.Equal(updated.Labels, []string{"backend"}) || updated.Priority != 2 || updated.Revision <= current.Revision {
			t.Fatalf("update: %v", updated)
		}
		refused(t, connect.CodeAborted, f.Clients.Items.UpdateItem, &v1.UpdateItemRequest{ItemId: item.Id, ExpectedRevision: current.Revision, Title: proto.String("stale")})
		// Concurrent updates of one revision have one winner.
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, title := range []string{"first", "second"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := f.Clients.Items.UpdateItem(t.Context(), connect.NewRequest(&v1.UpdateItemRequest{ItemId: item.Id, ExpectedRevision: updated.Revision, Title: proto.String(title)}))
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		won, stale := 0, 0
		for err := range errs {
			switch connect.CodeOf(err) {
			case connect.CodeAborted:
				stale++
			default:
				if err != nil {
					t.Fatal(err)
				}
				won++
			}
		}
		if won != 1 || stale != 1 {
			t.Fatalf("winners=%d stale=%d", won, stale)
		}
	})

	t.Run("notes and attachments", func(t *testing.T) {
		call(t, f.Clients.Items.AddNote, &v1.AddNoteRequest{ItemId: item.Id, Text: "remember"})
		attached := call(t, f.Clients.Items.Attach, &v1.AttachRequest{ItemId: item.Id, Name: "evidence.txt", Content: []byte("evidence")}).Note.Attachment
		if attached.Name != "evidence.txt" || attached.Size != 8 || attached.Sha256 == "" {
			t.Fatalf("attachment: %v", attached)
		}
		notes := call(t, f.Clients.Items.ListNotes, &v1.ListNotesRequest{ItemId: item.Id})
		if len(notes.Notes) != 2 || notes.Notes[0].Text != "remember" || notes.Notes[1].Attachment.GetSha256() != attached.Sha256 {
			t.Fatalf("notes: %v", notes.Notes)
		}
		if len(notes.Attachments) != 1 || notes.Attachments[0].Attachment.Name != "evidence.txt" {
			t.Fatalf("current attachments: %v", notes.Attachments)
		}
		stream, err := f.Clients.Items.ReadAttachment(t.Context(), connect.NewRequest(&v1.ReadAttachmentRequest{ItemId: item.Id, Sha256: attached.Sha256}))
		if err != nil {
			t.Fatal(err)
		}
		var content []byte
		for stream.Receive() {
			content = append(content, stream.Msg().Chunk...)
		}
		stream.Close()
		if stream.Err() != nil || string(content) != "evidence" {
			t.Fatalf("attachment content: %q %v", content, stream.Err())
		}
	})

	t.Run("search and view", func(t *testing.T) {
		search := call(t, f.Clients.Items.SearchItems, &v1.SearchItemsRequest{Query: "searchable"})
		if search.Total != 1 || search.Hits[0].Item.Id != item.Id || !strings.Contains(search.Hits[0].Excerpt, "searchable") {
			t.Fatalf("search: %v", search)
		}
		if none := call(t, f.Clients.Items.SearchItems, &v1.SearchItemsRequest{Query: "absent-word"}); none.Total != 0 {
			t.Fatalf("search for an absent word: %v", none)
		}
		view := call(t, f.Clients.Views.GetItemView, &v1.GetItemViewRequest{ItemId: item.Id})
		if view.Item.Id != item.Id || view.Description != "searchable description" || view.Observation == nil {
			t.Fatalf("view: %v", view)
		}
	})

	t.Run("read, unread and defer", func(t *testing.T) {
		observe := func() *v1.ItemObservation {
			return call(t, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: item.Id}).Observation
		}
		call(t, f.Clients.Items.MarkRead, &v1.MarkReadRequest{ItemId: item.Id, Unread: true})
		if !observe().Unread {
			t.Fatal("marked unread, reads read")
		}
		call(t, f.Clients.Items.MarkRead, &v1.MarkReadRequest{ItemId: item.Id})
		if o := observe(); o.Unread || o.Activity.GetLastReadAt() == nil {
			t.Fatalf("marked read: %v", o)
		}
		// Only an active item can be put off.
		refused(t, connect.CodeFailedPrecondition, f.Clients.Items.DeferItem, &v1.DeferItemRequest{ItemId: item.Id})
		call(t, f.Clients.Items.SetItemState, &v1.SetItemStateRequest{ItemId: item.Id, State: v1.ItemState_ITEM_STATE_ACTIVE})
		call(t, f.Clients.Items.DeferItem, &v1.DeferItemRequest{ItemId: item.Id})
		deferred := observe().Activity.GetLastDeferredAt()
		if deferred == nil {
			t.Fatal("defer not recorded")
		}
		// Deferring again with nothing new since is a no-op.
		call(t, f.Clients.Items.DeferItem, &v1.DeferItemRequest{ItemId: item.Id})
		if again := observe().Activity.GetLastDeferredAt(); !again.AsTime().Equal(deferred.AsTime()) {
			t.Fatalf("a repeated defer moved: %v -> %v", deferred.AsTime(), again.AsTime())
		}
	})

	// Blockers are durable intent; each records who set it, here another
	// item's agent with message access.
	t.Run("blockers record who set them and clear", func(t *testing.T) {
		setter := newItem(t, f, repo, "setter")
		agent, _ := f.AgentClients(t, setter.Id)
		call(t, f.Clients.Items.GrantAccess, &v1.GrantAccessRequest{Subject: "item:" + setter.Id, OnItemId: item.Id, Level: v1.AccessLevel_ACCESS_LEVEL_MESSAGE})
		external := call(t, agent.Items.AddBlocker, &v1.AddBlockerRequest{ItemId: item.Id, Kind: v1.BlockerKind_BLOCKER_KIND_EXTERNAL, Event: "review", FollowUp: "continue"})
		if external.Blocker.Id == "" || external.Blocker.FollowUp != "continue" || len(external.Item.Blockers) != 1 {
			t.Fatalf("external blocker: %v", external)
		}
		until := timestamppb.New(time.Now().Add(time.Hour))
		timed := call(t, f.Clients.Items.AddBlocker, &v1.AddBlockerRequest{ItemId: item.Id, Kind: v1.BlockerKind_BLOCKER_KIND_TIME, Until: until})
		if len(timed.Item.Blockers) != 2 {
			t.Fatalf("time blocker: %v", timed.Item.Blockers)
		}
		events := call(t, f.Clients.Items.ListItemEvents, &v1.ListItemEventsRequest{ItemId: item.Id}).Events
		setBy := ""
		for _, event := range events {
			if strings.Contains(event.Type, "blocker") && strings.Contains(event.PayloadJson, external.Blocker.Id) {
				setBy = event.Actor
			}
		}
		if setBy != "item:"+setter.Id {
			t.Fatalf("blocker set by %q, want the setter's agent; events: %v", setBy, events)
		}
		cleared := call(t, f.Clients.Items.ClearBlocker, &v1.ClearBlockerRequest{ItemId: item.Id, BlockerId: timed.Blocker.Id})
		if len(cleared.Item.Blockers) != 1 || cleared.Item.Blockers[0].Id != external.Blocker.Id {
			t.Fatalf("cleared one: %v", cleared.Item.Blockers)
		}
		if all := call(t, f.Clients.Items.ClearBlocker, &v1.ClearBlockerRequest{ItemId: item.Id, ClearAll: true, DropFollowUp: true}); len(all.Item.Blockers) != 0 || len(all.Item.FollowUps) != 0 {
			t.Fatalf("cleared all: %v", all.Item)
		}
	})

	t.Run("events page through the item's history", func(t *testing.T) {
		all := call(t, f.Clients.Items.ListItemEvents, &v1.ListItemEventsRequest{ItemId: item.Id}).Events
		if len(all) < 4 {
			t.Fatalf("history: %v", all)
		}
		first := call(t, f.Clients.Items.ListItemEvents, &v1.ListItemEventsRequest{ItemId: item.Id, PageSize: 2})
		if len(first.Events) != 2 || first.NextPageToken == "" || first.Events[0].Id != all[0].Id {
			t.Fatalf("first page: %v", first)
		}
		second := call(t, f.Clients.Items.ListItemEvents, &v1.ListItemEventsRequest{ItemId: item.Id, PageSize: 2, PageToken: first.NextPageToken})
		if len(second.Events) == 0 || second.Events[0].Id != all[2].Id {
			t.Fatalf("second page: %v", second)
		}
		refused(t, connect.CodeInvalidArgument, f.Clients.Items.ListItemEvents, &v1.ListItemEventsRequest{ItemId: item.Id, PageToken: "!"})
	})

	// A closed item keeps its slug: resolving it names the closed item, not
	// another that mentions it. A path in a checkout names its item.
	t.Run("closed items keep their slug, checkouts their item", func(t *testing.T) {
		other, checkout := activeItem(t, f, repo, "Auth refactor follow-up")
		if got := call(t, f.Clients.Items.ResolveItem, &v1.ResolveItemRequest{Path: checkout.Claim.Path}).ItemId; got != other.Id {
			t.Fatalf("resolve by checkout path = %s, want %s", got, other.Id)
		}
		closed := call(t, f.Clients.Lifecycle.CloseItem, &v1.CloseItemRequest{ItemId: item.Id, Resolution: v1.Resolution_RESOLUTION_COMPLETED})
		if closed.Job != nil {
			apitest.Wait(t, f.Clients, closed.Job)
		}
		if got := call(t, f.Clients.Items.ResolveItem, &v1.ResolveItemRequest{Selector: item.Slug}).ItemId; got != item.Id {
			t.Fatalf("ResolveItem(%q) = %s, want the closed %s", item.Slug, got, item.Id)
		}
	})

	t.Run("only a closed, released item is deleted", func(t *testing.T) {
		open := newItem(t, f, repo, "still open")
		refused(t, connect.CodeFailedPrecondition, f.Clients.Items.DeleteItem, &v1.DeleteItemRequest{ItemId: open.Id, Confirm: true})
		call(t, f.Clients.Items.DeleteItem, &v1.DeleteItemRequest{ItemId: item.Id, Confirm: true})
		refused(t, connect.CodeNotFound, f.Clients.Items.GetItem, &v1.GetItemRequest{ItemId: item.Id})
	})
}
