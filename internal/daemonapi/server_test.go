package daemonapi

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/daemonapp"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugins/bundled"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunIsolatedTests(m.Run)) }

type fixture struct {
	api     *Server
	core    *coordinator.Server
	clients *apitransport.Clients
	root    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "wi-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	cfg := config.Default()
	self, _ := os.Executable()
	_, host, _, err := bundled.Load(filepath.Join(root, "absent.toml"), self)
	if err != nil {
		t.Fatal(err)
	}
	core, err := coordinator.NewServerWithAgentSocket(filepath.Join(root, "data"), filepath.Join(root, "api.sock"), filepath.Join(root, "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(filepath.Join(root, "data"))
	a := app.New(st, gitpkg.New("git"))
	a.AgentRuntimeStateRoot = filepath.Join(root, "state")
	a.AgentRuntimeSocketRoot = filepath.Join(root, "run")
	a.Plugins = host
	host.ReadRepository = func(ctx context.Context, id string) (model.RepositorySettings, error) {
		value, err := core.LocalClient().RepositorySettings(ctx, id)
		return value.Settings, err
	}
	a.Preparation = host.Preparation(nil)
	daemonapp.Bind(a, st, core.LocalClient())
	a.LoadAgentProfiles = core.LocalClient().AgentProfiles
	_, err = core.LocalClient().ChangeAgentProfile(context.Background(), coordinator.ChangeAgentProfileCommand{Put: &model.AgentProfile{Name: "test", Driver: "pi"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	api, err := New(ctx, core, a, cfg, host, filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	core.ConfigureRepositorySettings(host.Registry.Options())
	core.SetBeforeClose(api.Close)
	clients, err := apitransport.NewClients(filepath.Join(root, "api.sock"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- core.ServeAPI(ctx, api.Handler()) }()
	t.Cleanup(func() {
		cancel()
		clients.Close()
		core.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := clients.System.GetInfo(ctx, connect.NewRequest(&v1.GetInfoRequest{}))
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &fixture{api: api, core: core, clients: clients, root: root}
}
func (f *fixture) repo(t *testing.T) string {
	t.Helper()
	source := filepath.Join(f.root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "-m", "initial"}} {
		cmd := exec.Command("git", append([]string{"-C", source}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	added, err := f.clients.Repositories.AddRepository(context.Background(), connect.NewRequest(&v1.AddRepositoryRequest{Name: "test", Source: source, Branch: "main", Local: true}))
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, f.clients, added.Msg.Job)
	if job.RepositoryId == "" {
		t.Fatal("repository import did not retain its identity")
	}
	return job.RepositoryId
}
func waitJob(t *testing.T, clients *apitransport.Clients, job *v1.Job) *v1.Job {
	t.Helper()
	if job == nil {
		t.Fatal("job absent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stream, err := clients.Jobs.WatchJob(ctx, connect.NewRequest(&v1.WatchJobRequest{JobId: job.Id}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var final *v1.Job
	for stream.Receive() {
		final = stream.Msg().Job
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if final == nil || final.State != v1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatalf("job failed: %v", final)
	}
	return final
}
func TestItemWorkspaceAndViewThroughAPI(t *testing.T) {
	f := newFixture(t)
	repo := f.repo(t)
	ctx := context.Background()
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "API item", Description: "searchable description", Labels: []string{"api"}, RequestId: "create-1"}))
	if err != nil {
		t.Fatal(err)
	}
	itemID := created.Msg.Item.Id
	duplicate, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "API item", Description: "searchable description", Labels: []string{"api"}, RequestId: "create-1"}))
	if err != nil || duplicate.Msg.Item.Id != itemID {
		t.Fatalf("creation retry: %v %v", duplicate, err)
	}
	resolved, err := f.clients.Items.ResolveItem(ctx, connect.NewRequest(&v1.ResolveItemRequest{Selector: created.Msg.Item.Slug}))
	if err != nil || resolved.Msg.ItemId != itemID {
		t.Fatalf("resolve: %v %v", resolved, err)
	}
	title := "renamed"
	updated, err := f.clients.Items.UpdateItem(ctx, connect.NewRequest(&v1.UpdateItemRequest{ItemId: itemID, ExpectedRevision: created.Msg.Item.Revision, Title: &title}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.clients.Items.UpdateItem(ctx, connect.NewRequest(&v1.UpdateItemRequest{ItemId: itemID, ExpectedRevision: created.Msg.Item.Revision, Title: &title}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale update: %v", err)
	}
	_, err = f.clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: itemID, State: v1.ItemState_ITEM_STATE_ACTIVE, ExpectedRevision: updated.Msg.Item.Revision}))
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := f.clients.Workspaces.EnsureWorkspace(ctx, connect.NewRequest(&v1.EnsureWorkspaceRequest{ItemId: itemID}))
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.clients, workspace.Msg.Job)
	_, err = f.clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: itemID, State: v1.ItemState_ITEM_STATE_BACKLOG}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("state with claim: %v", err)
	}
	_, err = f.clients.Items.AddNote(ctx, connect.NewRequest(&v1.AddNoteRequest{ItemId: itemID, Text: "remember"}))
	if err != nil {
		t.Fatal(err)
	}
	attached, err := f.clients.Items.Attach(ctx, connect.NewRequest(&v1.AttachRequest{ItemId: itemID, Name: "evidence.txt", Content: []byte("evidence")}))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := f.clients.Items.ReadAttachment(ctx, connect.NewRequest(&v1.ReadAttachmentRequest{ItemId: itemID, Sha256: attached.Msg.Note.Attachment.Sha256}))
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
	view, err := f.clients.Views.GetItemView(ctx, connect.NewRequest(&v1.GetItemViewRequest{ItemId: itemID}))
	if err != nil {
		t.Fatal(err)
	}
	if view.Msg.Item.Id != itemID || view.Msg.Description != "searchable description" || view.Msg.Setup == nil {
		t.Fatalf("view: %v", view.Msg)
	}
	search, err := f.clients.Items.SearchItems(ctx, connect.NewRequest(&v1.SearchItemsRequest{Query: "searchable"}))
	if err != nil || search.Msg.Total != 1 {
		t.Fatalf("search: %v %v", search, err)
	}
	_, err = f.clients.Workspaces.ReleaseWorkspace(ctx, connect.NewRequest(&v1.ReleaseWorkspaceRequest{ItemId: itemID}))
	if err != nil {
		t.Fatal(err)
	}
	closed, err := f.clients.Lifecycle.CloseItem(ctx, connect.NewRequest(&v1.CloseItemRequest{ItemId: itemID, Resolution: v1.Resolution_RESOLUTION_COMPLETED}))
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.clients, closed.Msg.Job)
	final, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: itemID}))
	if err != nil || final.Msg.Item.State != v1.ItemState_ITEM_STATE_CLOSED {
		t.Fatalf("close: %v %v", final, err)
	}
}
func TestConcurrentFencedUpdatesHaveOneWinner(t *testing.T) {
	f := newFixture(t)
	repo := f.repo(t)
	created, err := f.clients.Items.CreateItem(context.Background(), connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo, Title: "race"}))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, title := range []string{"first", "second"} {
		wg.Add(1)
		go func(title string) {
			defer wg.Done()
			_, err := f.clients.Items.UpdateItem(context.Background(), connect.NewRequest(&v1.UpdateItemRequest{ItemId: created.Msg.Item.Id, ExpectedRevision: created.Msg.Item.Revision, Title: &title}))
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
func TestRestrictedEndpointRejectsStreamsAndLifecycle(t *testing.T) {
	f := newFixture(t)
	client, err := apitransport.NewClients(filepath.Join(f.root, "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	info, err := client.System.GetInfo(context.Background(), connect.NewRequest(&v1.GetInfoRequest{}))
	if err != nil || info.Msg.Caller.GetAccess() != v1.CallerAccess_CALLER_ACCESS_AGENT || info.Msg.DataRoot != "" {
		t.Fatalf("agent info: %v %v", info, err)
	}
	_, err = client.Lifecycle.StartItem(context.Background(), connect.NewRequest(&v1.StartItemRequest{ItemId: "01KZYHGDCECSFS4BJ2SNTQP49W"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatal(err)
	}
	stream, err := client.Events.WatchChanges(context.Background(), connect.NewRequest(&v1.WatchChangesRequest{}))
	if err == nil {
		defer stream.Close()
		stream.Receive()
		err = stream.Err()
	}
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("agent stream: %v", err)
	}
}
func TestChangeStreamInitialAndCancellation(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := f.clients.Events.WatchChanges(ctx, connect.NewRequest(&v1.WatchChangesRequest{}))
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

func TestSetupBlocksAgentOnlyWhileRunningOrFailed(t *testing.T) {
	for _, test := range []struct {
		setup *v1.SetupRun
		want  bool
	}{
		{nil, false},
		{&v1.SetupRun{State: v1.SetupState_SETUP_STATE_SUCCEEDED}, false},
		{&v1.SetupRun{State: v1.SetupState_SETUP_STATE_RUNNING}, true},
		{&v1.SetupRun{State: v1.SetupState_SETUP_STATE_FAILED}, true},
	} {
		if got := setupBlocksAgent(test.setup); got != test.want {
			t.Errorf("setupBlocksAgent(%v) = %v, want %v", test.setup, got, test.want)
		}
	}
}
