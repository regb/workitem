package daemonapi_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/daemonapi"
)

// An item's agent may do what its access says: everything on its own item
// and its delegations, and on others what entries grant, up to a level.
func TestItemsAgentsAreHeldToTheirAccess(t *testing.T) {
	f := apitest.New(t)
	ctx := context.Background()
	repo := f.Repository(t, "test")
	create := func(title string) string {
		value, err := f.Clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: title}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Item.Id
	}
	a, b := create("a"), create("b")
	agent, _ := f.AgentClients(t, a)
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
	if _, err := f.Clients.Items.RevokeAccess(ctx, connect.NewRequest(&v1.RevokeAccessRequest{EntryId: coordinator.DefaultAccessID})); err != nil {
		t.Fatal(err)
	}
	_, err = agent.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: b}))
	if !denied(err) {
		t.Fatalf("viewing another item without access: %v", err)
	}
	// Its own repository's environment it already runs with; another's
	// values, and the profiles' settings, are a person's.
	if _, err := agent.Repositories.GetEnvironment(ctx, connect.NewRequest(&v1.GetEnvironmentRequest{RepositoryId: repo})); err != nil {
		t.Fatalf("own repository's environment: %v", err)
	}
	if _, err := agent.Repositories.GetEnvironment(ctx, connect.NewRequest(&v1.GetEnvironmentRequest{RepositoryId: "01M8CA5QR6R1R68TKH4QFXKP1H"})); !denied(err) {
		t.Fatalf("another repository's environment: %v", err)
	}
	profiles, profilesErr := agent.Profiles.ListAgentProfiles(ctx, connect.NewRequest(&v1.ListAgentProfilesRequest{}))
	if profilesErr != nil || len(profiles.Msg.Profiles) == 0 {
		t.Fatalf("profiles: %v %v", profiles, profilesErr)
	}
	for _, p := range profiles.Msg.Profiles {
		if p.Config != nil {
			t.Fatalf("an agent read profile %s's settings", p.Name)
		}
	}
	if mine, err := f.Clients.Profiles.ListAgentProfiles(ctx, connect.NewRequest(&v1.ListAgentProfilesRequest{})); err != nil || mine.Msg.Profiles[0].Config == nil {
		t.Fatalf("the operator's profiles: %v %v", mine, err)
	}
	// Requests that name their item inside a nested request are held to it.
	if _, err := agent.Reviews.GetRevisionPatch(ctx, connect.NewRequest(&v1.GetRevisionPatchRequest{Comparison: &v1.GetRevisionReviewRequest{ItemId: b, RevisionId: "r1"}})); !denied(err) {
		t.Fatalf("another item's revision patch without access: %v", err)
	}
	if _, err := agent.Reviews.GetRevisionReviewFile(ctx, connect.NewRequest(&v1.GetRevisionReviewFileRequest{Comparison: &v1.GetRevisionReviewRequest{ItemId: b, RevisionId: "r1"}, Path: "a"})); !denied(err) {
		t.Fatalf("another item's revision file without access: %v", err)
	}
	if _, err := agent.Reviews.GetRevisionPatch(ctx, connect.NewRequest(&v1.GetRevisionPatchRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("revision patch naming no item: %v", err)
	}
	var refusal *connect.Error
	errors.As(err, &refusal)
	if detail, _ := refusal.Details()[0].Value(); detail.(*v1.AccessDenied).Needed != v1.AccessLevel_ACCESS_LEVEL_VIEW {
		t.Fatalf("detail: %v", detail)
	}
	// The change feed and jobs name only what it may view.
	asAgent := daemonapi.AsItemAgent(ctx, a)
	if got := f.API().Viewable(asAgent, []string{a, b, c}); len(got) != 2 || got[0] != a || got[1] != c {
		t.Fatalf("viewable: %v", got)
	}
	if jobs := f.API().ViewableJobs(asAgent, []*v1.Job{{ItemId: b}, {ItemId: c}, {}}); len(jobs) != 1 || jobs[0].ItemId != c {
		t.Fatalf("viewable jobs: %v", jobs)
	}
	list, err := agent.Items.ListItems(ctx, connect.NewRequest(&v1.ListItemsRequest{Owner: ""}))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range list.Msg.Items {
		if row.Id == b {
			t.Fatalf("the agent's list shows an item it may not view: %v", list)
		}
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
		if _, err := f.Clients.Items.GrantAccess(ctx, connect.NewRequest(&v1.GrantAccessRequest{Subject: subject, OnItemId: on, Level: level})); err != nil {
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
	// A message to a backlog item would start it, which takes control.
	_, err = agent.Agents.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: b, Text: "start please"}))
	if !denied(err) {
		t.Fatalf("starting a backlog item with message access: %v", err)
	}
	errors.As(err, &refusal)
	if detail, _ := refusal.Details()[0].Value(); detail.(*v1.AccessDenied).Needed != v1.AccessLevel_ACCESS_LEVEL_CONTROL {
		t.Fatalf("detail: %v", detail)
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
		"secret configuration": func() error {
			_, err := agent.System.GetConfig(ctx, connect.NewRequest(&v1.GetConfigRequest{ShowSensitive: true}))
			return err
		},
		"secret repository settings": func() error {
			_, err := agent.Repositories.GetRepositorySettings(ctx, connect.NewRequest(&v1.GetRepositorySettingsRequest{RepositoryId: repo, ShowSensitive: true}))
			return err
		},
		"reset to upstream": func() error {
			_, err := agent.Repositories.SyncRepository(ctx, connect.NewRequest(&v1.SyncRepositoryRequest{RepositoryId: repo, Mode: v1.SyncMode_SYNC_MODE_RESET_TO_UPSTREAM, Confirm: true}))
			return err
		},
	} {
		if err := call(); !denied(err) {
			t.Errorf("%s by an agent: %v", name, err)
		}
	}
	entries, err := f.Clients.Items.ListAccess(ctx, connect.NewRequest(&v1.ListAccessRequest{}))
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

// A create-and-start names its repository inside its item: the daemon
// checks it, and holds the repository, as it does for a creation.
func TestACreateAndStartNamesItsRepository(t *testing.T) {
	f := apitest.New(t)
	_, err := f.Clients.Lifecycle.CreateAndStartItem(context.Background(), connect.NewRequest(&v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: "not-an-id", Title: "x"}}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "repository_id") {
		t.Fatalf("create-and-start with an invalid repository: %v", err)
	}
}
