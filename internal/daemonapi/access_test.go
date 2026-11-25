package daemonapi

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/credential"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Every procedure is decided for an item's agent: on an item at a level,
// for a person only, or open to every item. Adding an RPC forces it.
func TestEveryProcedureIsClassifiedForItemsAgents(t *testing.T) {
	_ = v1.File_wi_core_v1_items_proto
	protoregistry.GlobalFiles.RangeFilesByPackage("wi.core.v1", func(file protoreflect.FileDescriptor) bool {
		services := file.Services()
		for i := range services.Len() {
			methods := services.Get(i).Methods()
			for j := range methods.Len() {
				procedure := "/" + string(services.Get(i).FullName()) + "/" + string(methods.Get(j).Name())
				_, level := itemLevels[procedure]
				_, users := usersOnly[procedure]
				count := 0
				for _, in := range []bool{level, users, anyItem[procedure]} {
					if in {
						count++
					}
				}
				if count != 1 {
					t.Errorf("%s must be in exactly one of itemLevels, usersOnly and anyItem", procedure)
				}
				if level && methods.Get(j).Input().Fields().ByName("item_id") == nil {
					t.Errorf("%s has a level but names no item", procedure)
				}
			}
		}
		return true
	})
}

// An item's agent may do what its access says: everything on its own item
// and its delegations, and on others what entries grant, up to a level.
func TestItemsAgentsAreHeldToTheirAccess(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	repo := f.repo(t)
	create := func(title string) string {
		value, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: title}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Item.Id
	}
	a, b := create("a"), create("b")
	agent, _ := f.agentClients(t, a)
	child, err := agent.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "c"}))
	if err != nil {
		t.Fatal(err)
	}
	c := child.Msg.Item.Id
	denied := func(err error) bool {
		var refusal *connect.Error
		return connect.CodeOf(err) == connect.CodePermissionDenied && errors.As(err, &refusal)
	}
	// By default every item's agent may manage every item, as before.
	if _, err := agent.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: b})); err != nil {
		t.Fatalf("default access: %v", err)
	}
	if _, err := f.clients.Items.RevokeAccess(ctx, connect.NewRequest(&v1.RevokeAccessRequest{EntryId: coordinator.DefaultAccessID})); err != nil {
		t.Fatal(err)
	}
	_, err = agent.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: b}))
	if !denied(err) {
		t.Fatalf("viewing another item without access: %v", err)
	}
	var refusal *connect.Error
	errors.As(err, &refusal)
	if detail, _ := refusal.Details()[0].Value(); detail.(*v1.AccessDenied).Needed != v1.AccessLevel_ACCESS_LEVEL_VIEW {
		t.Fatalf("detail: %v", detail)
	}
	// The change feed and jobs name only what it may view.
	asAgent := context.WithValue(ctx, identityKey{}, identity{principal: principal{kind: credential.Item, itemID: a}})
	if got := f.api.viewable(asAgent, []string{a, b, c}); len(got) != 2 || got[0] != a || got[1] != c {
		t.Fatalf("viewable: %v", got)
	}
	if jobs := f.api.viewableJobs(asAgent, []*v1.Job{{ItemId: b}, {ItemId: c}, {}}); len(jobs) != 1 || jobs[0].ItemId != c {
		t.Fatalf("viewable jobs: %v", jobs)
	}
	list, err := agent.Items.ListItems(ctx, connect.NewRequest(&v1.ListItemsRequest{Owner: ""}))
	if err != nil || len(list.Msg.Items) != 1 || list.Msg.Items[0].Id != a {
		t.Fatalf("the agent's list: %v %v", list, err)
	}
	// Its own item and its delegations stay its own.
	for _, id := range []string{a, c} {
		if _, err := agent.Items.UpdateItem(ctx, connect.NewRequest(&v1.UpdateItemRequest{ItemId: id, AddLabels: []string{"mine"}})); err != nil {
			t.Fatalf("own item %s: %v", id, err)
		}
	}
	// A grant lets it as far as its level.
	grant := func(subject, on string, level v1.AccessLevel) {
		t.Helper()
		if _, err := f.clients.Items.GrantAccess(ctx, connect.NewRequest(&v1.GrantAccessRequest{Subject: subject, OnItemId: on, Level: level})); err != nil {
			t.Fatal(err)
		}
	}
	grant("item:"+a, b, v1.AccessLevel_ACCESS_LEVEL_VIEW)
	if _, err := agent.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: b})); err != nil {
		t.Fatalf("view granted: %v", err)
	}
	if _, err := agent.Items.AddNote(ctx, connect.NewRequest(&v1.AddNoteRequest{ItemId: b, Text: "hi"})); !denied(err) {
		t.Fatalf("message with view: %v", err)
	}
	grant("item:*", "*", v1.AccessLevel_ACCESS_LEVEL_MESSAGE)
	if _, err := agent.Items.AddNote(ctx, connect.NewRequest(&v1.AddNoteRequest{ItemId: b, Text: "hi"})); err != nil {
		t.Fatalf("message for every item: %v", err)
	}
	if _, err := agent.Items.UpdateItem(ctx, connect.NewRequest(&v1.UpdateItemRequest{ItemId: b, AddLabels: []string{"x"}})); !denied(err) {
		t.Fatalf("control with message: %v", err)
	}
	if _, err := agent.Lifecycle.CloseItem(ctx, connect.NewRequest(&v1.CloseItemRequest{ItemId: b, Resolution: v1.Resolution_RESOLUTION_COMPLETED})); !denied(err) {
		t.Fatalf("close with message: %v", err)
	}
	// Decisions are a person's.
	for name, call := range map[string]func() error{
		"list access": func() error {
			_, err := agent.Items.ListAccess(ctx, connect.NewRequest(&v1.ListAccessRequest{}))
			return err
		},
		"grant": func() error {
			_, err := agent.Items.GrantAccess(ctx, connect.NewRequest(&v1.GrantAccessRequest{Subject: "item:" + a, OnItemId: "*", Level: v1.AccessLevel_ACCESS_LEVEL_MANAGE}))
			return err
		},
		"delete": func() error {
			_, err := agent.Items.DeleteItem(ctx, connect.NewRequest(&v1.DeleteItemRequest{ItemId: b, Confirm: true}))
			return err
		},
		"shutdown": func() error {
			_, err := agent.System.Shutdown(ctx, connect.NewRequest(&v1.ShutdownRequest{}))
			return err
		},
	} {
		if err := call(); !denied(err) {
			t.Errorf("%s by an agent: %v", name, err)
		}
	}
	entries, err := f.clients.Items.ListAccess(ctx, connect.NewRequest(&v1.ListAccessRequest{}))
	if err != nil || len(entries.Msg.Entries) != 2 {
		t.Fatalf("entries: %v %v", entries, err)
	}
	// Streams are held to the same access.
	stream, err := agent.Agents.WatchLiveContent(ctx, connect.NewRequest(&v1.WatchLiveContentRequest{ItemId: b}))
	if err == nil {
		stream.Receive()
		err = stream.Err()
		stream.Close()
	}
	if err != nil && connect.CodeOf(err) == connect.CodePermissionDenied {
		t.Fatalf("live content with message access: %v", err)
	}
}
