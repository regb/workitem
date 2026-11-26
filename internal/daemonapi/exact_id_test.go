package daemonapi_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
)

// Every RPC takes an item by its whole ID. A slug, title or ID prefix is
// refused rather than matched, so a stale or ambiguous name can never act on
// another item; ResolveItem alone turns names into IDs.
func TestItemsAreNamedOnlyByTheirID(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	created, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: f.Repository(t, "test"), Title: "Exact names"}))
	if err != nil {
		t.Fatal(err)
	}
	item := created.Msg.Item
	for _, name := range []string{item.Slug, item.Id[:12], "Exact names"} {
		if _, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: name})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("GetItem(%q): %v", name, err)
		}
		if _, err := f.Clients.Items.AddNote(ctx, connect.NewRequest(&v1.AddNoteRequest{ItemId: name, Text: "x"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("AddNote(%q): %v", name, err)
		}
	}
	resolved, err := f.Clients.Items.ResolveItem(ctx, connect.NewRequest(&v1.ResolveItemRequest{Selector: item.Slug}))
	if err != nil || resolved.Msg.ItemId != item.Id {
		t.Fatalf("ResolveItem(%q) = %v %v", item.Slug, resolved, err)
	}
	if _, err := f.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: item.Id})); err != nil {
		t.Fatal(err)
	}
}
