package daemonapi

import (
	"context"
	"sync"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func TestRepositorySettingRevisionIsStrictAndAtomic(t *testing.T) {
	f := newFixture(t)
	repository := f.repo(t)
	var group sync.WaitGroup
	codes := make(chan connect.Code, 2)
	for _, run := range []string{"first", "second"} {
		group.Add(1)
		go func(run string) {
			defer group.Done()
			_, err := f.clients.Repositories.SetRepositorySetting(context.Background(), connect.NewRequest(&v1.SetRepositorySettingRequest{RepositoryId: repository, Key: "plugins.setup.run", Value: &v1.SettingValue{Value: &v1.SettingValue_StringValue{StringValue: run}}}))
			if err == nil {
				codes <- 0
			} else {
				codes <- connect.CodeOf(err)
			}
		}(run)
	}
	group.Wait()
	close(codes)
	wins, aborts := 0, 0
	for code := range codes {
		switch code {
		case 0:
			wins++
		case connect.CodeAborted:
			aborts++
		default:
			t.Fatalf("unexpected setting status: %v", code)
		}
	}
	if wins != 1 || aborts != 1 {
		t.Fatalf("wins=%d aborted=%d", wins, aborts)
	}
}
func TestInvalidSetupDoesNotWriteEarlierFields(t *testing.T) {
	f := newFixture(t)
	repository := f.repo(t)
	before, err := f.clients.Repositories.GetCheckoutSetup(context.Background(), connect.NewRequest(&v1.GetCheckoutSetupRequest{RepositoryId: repository}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.clients.Repositories.UpdateCheckoutSetup(context.Background(), connect.NewRequest(&v1.UpdateCheckoutSetupRequest{RepositoryId: repository, ExpectedRevision: before.Msg.Revision, Setup: &v1.CheckoutSetup{Run: "must not be persisted", Trust: 99, TimeoutSeconds: 0}}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid setup: %v", err)
	}
	after, err := f.clients.Repositories.GetCheckoutSetup(context.Background(), connect.NewRequest(&v1.GetCheckoutSetupRequest{RepositoryId: repository}))
	if err != nil {
		t.Fatal(err)
	}
	if after.Msg.Revision != before.Msg.Revision || after.Msg.Setup.Run != before.Msg.Setup.Run {
		t.Fatalf("partial validation write: %v", after.Msg)
	}
}
func TestProfileFencesAndDefaultCannotBeDeleted(t *testing.T) {
	f := newFixture(t)
	profiles, err := f.clients.Profiles.ListAgentProfiles(context.Background(), connect.NewRequest(&v1.ListAgentProfilesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.clients.Profiles.SetDefaultAgentProfile(context.Background(), connect.NewRequest(&v1.SetDefaultAgentProfileRequest{Name: "test", ExpectedRevision: 0}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("zero bypassed profile fence: %v", err)
	}
	_, err = f.clients.Profiles.DeleteAgentProfile(context.Background(), connect.NewRequest(&v1.DeleteAgentProfileRequest{Name: "test", ExpectedRevision: profiles.Msg.Revision}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("deleted default profile: %v", err)
	}
}
func TestMalformedStartDoesNotCreateAnItem(t *testing.T) {
	f := newFixture(t)
	repository := f.repo(t)
	_, err := f.clients.Lifecycle.CreateAndStartItem(context.Background(), connect.NewRequest(&v1.CreateAndStartItemRequest{Item: &v1.CreateItemRequest{RepositoryId: repository, Title: "must not exist"}, Start: &v1.StartOptions{SendNothing: true, Prompt: "conflict"}}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid start: %v", err)
	}
	items, err := f.clients.Items.ListItems(context.Background(), connect.NewRequest(&v1.ListItemsRequest{}))
	if err != nil || len(items.Msg.Items) != 0 {
		t.Fatalf("invalid request had effects: %v %v", items, err)
	}
}
func TestRestrictedCaptureCannotLaunchTitleWork(t *testing.T) {
	f := newFixture(t)
	repository := f.repo(t)
	clients, err := apitransport.NewClients(f.root + "/agent.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer clients.Close()
	_, err = clients.Items.CreateItem(context.Background(), connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repository, Description: "title work", GenerateTitle: true}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("restricted title launch: %v", err)
	}
}
func TestDeepWorkActivationHonorsDaemonCapacity(t *testing.T) {
	f := newFixture(t)
	f.api.Config.DeepWork.MaxActive = 1
	repository := f.repo(t)
	create := func(title string) string {
		value, err := f.clients.Items.CreateItem(context.Background(), connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repository, Title: title, DeepWork: true}))
		if err != nil {
			t.Fatal(err)
		}
		return value.Msg.Item.Id
	}
	first, second := create("one"), create("two")
	if _, err := f.clients.Items.SetItemState(context.Background(), connect.NewRequest(&v1.SetItemStateRequest{ItemId: first, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	_, err := f.clients.Items.SetItemState(context.Background(), connect.NewRequest(&v1.SetItemStateRequest{ItemId: second, State: v1.ItemState_ITEM_STATE_ACTIVE}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("capacity bypassed: %v", err)
	}
	_, err = f.clients.Items.SetItemState(context.Background(), connect.NewRequest(&v1.SetItemStateRequest{ItemId: second, State: v1.ItemState_ITEM_STATE_ACTIVE, OverrideCapacity: true}))
	if err != nil {
		t.Fatalf("operator capacity override: %v", err)
	}
}
func TestProfileChangeNotifiesGlobalFeed(t *testing.T) {
	f := newFixture(t)
	profiles, err := f.core.LocalClient().ChangeAgentProfile(context.Background(), coordinator.ChangeAgentProfileCommand{Put: &model.AgentProfile{Name: "other", Driver: "pi"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := f.clients.Events.WatchChanges(ctx, connect.NewRequest(&v1.WatchChangesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Receive() {
		t.Fatal(stream.Err())
	}
	_, err = f.clients.Profiles.DeleteAgentProfile(ctx, connect.NewRequest(&v1.DeleteAgentProfileRequest{Name: "other", ExpectedRevision: uint32(profiles.Revision)}))
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Receive() || !stream.Msg().ProfilesChanged || !stream.Msg().All {
		t.Fatalf("missing profile invalidation: %v %v", stream.Msg(), stream.Err())
	}
}
