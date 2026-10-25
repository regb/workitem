package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	"github.com/regb/workitem/internal/testutil"
	webaccess "github.com/regb/workitem/internal/web"
)

// The fixture validates configuration but deliberately cannot launch a conversation.
type webCreationBackend struct {
	app.ConversationBackend
	validationErr error
}

func (b webCreationBackend) ValidateConfig(context.Context, string, json.RawMessage) error {
	return b.validationErr
}

type webTitleBackend struct {
	webCreationBackend
	generate func(context.Context) (string, error)
}

func (b webTitleBackend) GenerateTitle(ctx context.Context, _ string, _ string, _ json.RawMessage) (string, error) {
	return b.generate(ctx)
}
func (webCreationBackend) NewConversation(context.Context, string, string, json.RawMessage) (model.Conversation, error) {
	return model.Conversation{}, fmt.Errorf("fixture startup failure")
}

func TestWebInitialPromptDispositionRequiresNativeCommandIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, eventType, runtimeID, commandID string
		matched, failed                       bool
	}{
		{"transport handoff", agent.EventCommandAccepted, "owner", "", false, false},
		{"other command", agent.EventCommandRejected, "owner", "another", false, false},
		{"superseded owner", agent.EventCommandRejected, "old-owner", "initial", false, false},
		{"native accepts", agent.EventCommandAccepted, "owner", "initial", true, false},
		{"native rejects", agent.EventCommandRejected, "owner", "initial", true, true},
		{"runtime fails", agent.EventRuntimeFailed, "owner", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]string{"runtime_id": tc.runtimeID, "command_id": tc.commandID})
			matched, err := webInitialPromptDisposition(coordinator.DomainEvent{Type: tc.eventType, Payload: payload}, "owner", "initial")
			if matched != tc.matched || (err != nil) != tc.failed {
				t.Fatalf("matched=%v err=%v", matched, err)
			}
		})
	}
}

func TestWebCreationRetainsItemWhenStartupFails(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "Fixture"}} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", output, err)
		}
	}
	native := store.New(t.TempDir())
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	socket, err := coordinator.SocketPath(testutil.ShortTempDir(t), native.Root)
	if err != nil {
		t.Fatal(err)
	}
	server, err := coordinator.NewServer(native.Root, socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { server.Close(); <-done })
	client := &coordinator.Client{SocketPath: socket}
	application := app.New(native, gitpkg.New("git"))
	application.ConversationBackends["pi"] = webCreationBackend{}
	application.DefaultAgent = "pi"
	application.AgentsConfig["pi"] = config.AgentDefinitionConfig{Driver: "pi", Config: map[string]any{}}
	application.AgentsConfig["review"] = config.AgentDefinitionConfig{Driver: "pi", Label: "Pi Review"}
	BindCoordinatorApp(application, native, client)
	cfg := Config{App: application, Coordinator: client, Env: map[string]string{}, CWD: repo, Stdout: io.Discard, Stderr: io.Discard, RequireOwnedRepository: true}
	if err := runRepository(ctx, []string{"add", "fixture", "--from", repo}, cfg, false); err != nil {
		t.Fatal(err)
	}
	repositories, err := client.ListRepositories(ctx)
	if err != nil || len(repositories) != 1 {
		t.Fatalf("registry=%+v err=%v", repositories, err)
	}
	var source coordinator.RepositorySourceResult
	if err := client.Call(ctx, coordinator.MethodRepositorySource, coordinator.RepositorySourceRequest{Selector: repositories[0].ID}, &source); err != nil {
		t.Fatal(err)
	}
	seed, err := createCoordinatorWorkItem(ctx, cfg, app.NewWorkItemOptions{Title: "Existing project", CWD: repo, Repository: &model.Repository{ID: source.Repository.ID, Name: source.Repository.Name, IntegrationBranch: source.Repository.IntegrationBranch, RootAtCreation: source.GitDir, GitCommonDir: source.GitDir, CreatedFromCommit: source.Commit}})
	if err != nil {
		t.Fatal(err)
	}
	backend := webBackend{cfg}
	profiles, err := backend.Agents(ctx)
	if err != nil || len(profiles) != 2 || profiles[0].Name != "pi" || profiles[1].Name != "review" || profiles[1].Label != "Pi Review" {
		t.Fatalf("agent profiles=%+v err=%v", profiles, err)
	}
	if out := backend.Create(ctx, webaccess.Create{ProjectID: "/etc", Prompt: "Do not run"}); out.Error == "" || out.Observed != nil || out.CreationUncertain {
		t.Fatalf("unknown project reached creation: %+v", out)
	}
	application.ConversationBackends["pi"] = webCreationBackend{validationErr: fmt.Errorf("invalid profile")}
	rejected := backend.Create(ctx, webaccess.Create{ProjectID: repositories[0].ID, Prompt: "Do not create"})
	if rejected.Error == "" || rejected.Observed != nil || rejected.CreationUncertain {
		t.Fatalf("invalid config reached creation: %+v", rejected)
	}
	projection, err := client.ManifestProjection(ctx)
	if err != nil || len(projection.Manifests) != 1 {
		t.Fatalf("preflight created durable state: %+v %v", projection, err)
	}
	application.ConversationBackends["pi"] = webCreationBackend{}
	// The fixture backend deliberately fails startup after
	// daemon-owned creation and the lifecycle transition have committed.
	accepted := backend.Create(ctx, webaccess.Create{ProjectID: model.RepositoryKey(seed.Manifest.Repository), Agent: "review", Prompt: "Self-contained initial request"})
	if accepted.Error != "" || accepted.Observed == nil || accepted.Observed.State != model.StateActive {
		t.Fatalf("creation not durable before startup: %+v", accepted)
	}
	if accepted.Observed.Title != model.UntitledTitle || accepted.Observed.Slug != model.IDDerivedSlug(accepted.Observed.ID) {
		t.Fatalf("creation does not use main untitled policy: %+v", accepted.Observed)
	}
	initialSlug, initialBranch := accepted.Observed.Slug, accepted.Observed.Checkout.Specification.Branch
	if accepted.Revision == 0 {
		t.Fatalf("creation recorded no revision to fence its start on: %+v", accepted)
	}
	out := backend.StartCreated(ctx, accepted.Observed.ID, accepted.Revision)
	if out.Error == "" || out.Observed == nil || out.Observed.ID == seed.Manifest.ID || out.Observed.State != model.StateActive || out.CreationUncertain {
		t.Fatalf("lost durable partial outcome: %+v", out)
	}
	shown, err := application.ShowWorkItem(ctx, app.ResolveOptions{Selector: out.Observed.ID})
	if err != nil || shown.Description != "Self-contained initial request" || shown.Manifest.Agent != "review" || shown.Manifest.Repository.GitCommonDir != seed.Manifest.Repository.GitCommonDir {
		t.Fatalf("created ticket: %+v, %v", shown, err)
	}
	if shown.Manifest.Title != "Self-contained initial request" || shown.Manifest.Slug != initialSlug || shown.Manifest.Checkout.Specification.Branch != initialBranch {
		t.Fatalf("display-title edit changed stable naming: %+v", shown.Manifest)
	}
	shelved := backend.Shelve(ctx, shown.Manifest.ID)
	if shelved.Error != "" || shelved.Observed == nil || shelved.Observed.State != model.StateBacklog {
		t.Fatalf("web shelve: %+v", shelved)
	}
	// A queued intent must not restore a conversation archived before startup.
	interrupted := backend.Create(ctx, webaccess.Create{ProjectID: model.RepositoryKey(seed.Manifest.Repository), Prompt: "Do not restore an archived conversation"})
	if interrupted.Error != "" || interrupted.Observed == nil {
		t.Fatalf("creation: %+v", interrupted)
	}
	if _, err := coordinatorStateTransition(ctx, cfg, interrupted.Observed.ID, model.StateClosed, "work_item.archived", false); err != nil {
		t.Fatal(err)
	}
	if aborted := backend.StartCreated(ctx, interrupted.Observed.ID, interrupted.Revision); aborted.Error == "" || aborted.Observed.State != model.StateClosed || aborted.Observed.PrimaryConversation != nil {
		t.Fatalf("queued startup overrode archive: %+v", aborted)
	}
	// A concurrent edit while naming must win and prevent startup.
	concurrent := backend.Create(ctx, webaccess.Create{ProjectID: repositories[0].ID, Prompt: "Original task"})
	if concurrent.Error != "" || concurrent.Observed == nil {
		t.Fatalf("create: %+v", concurrent)
	}
	application.ConversationBackends["pi"] = webTitleBackend{generate: func(ctx context.Context) (string, error) {
		before, err := client.CanonicalManifest(ctx, concurrent.Observed.ID)
		if err != nil {
			return "", err
		}
		title := "User chosen title"
		_, err = client.EditItem(ctx, coordinator.EditItemRequest{Command: coordinator.ManifestCommand{ID: "concurrent-web-title", ProtocolVersion: coordinator.ProtocolVersion, Type: coordinator.CommandEdit, ItemID: before.Manifest.ID, ExpectedRevision: &before.Revision, Actor: "user", Edit: &coordinator.MetadataEdit{Title: &title}}})
		return "Generated title", err
	}}
	blocked := backend.StartCreated(ctx, concurrent.Observed.ID, concurrent.Revision)
	if blocked.Error == "" || blocked.Observed == nil || blocked.Observed.Title != "User chosen title" || blocked.Observed.Checkout.Claim != nil || blocked.Observed.PrimaryConversation != nil || blocked.Observed.Slug != concurrent.Observed.Slug || blocked.Observed.Checkout.Specification.Branch != concurrent.Observed.Checkout.Specification.Branch {
		t.Fatalf("background startup overrode edit: %+v", blocked)
	}
	// An edit made while the start was queued wins as well.
	edited := backend.Create(ctx, webaccess.Create{ProjectID: repositories[0].ID, Prompt: "Queued task"})
	if edited.Error != "" || edited.Observed == nil {
		t.Fatalf("create: %+v", edited)
	}
	before, err := client.CanonicalManifest(ctx, edited.Observed.ID)
	if err != nil {
		t.Fatal(err)
	}
	renamed := "Renamed while queued"
	if _, err := client.EditItem(ctx, coordinator.EditItemRequest{Command: coordinator.ManifestCommand{ID: "queued-web-title", ProtocolVersion: coordinator.ProtocolVersion, Type: coordinator.CommandEdit, ItemID: before.Manifest.ID, ExpectedRevision: &before.Revision, Actor: "user", Edit: &coordinator.MetadataEdit{Title: &renamed}}}); err != nil {
		t.Fatal(err)
	}
	if refused := backend.StartCreated(ctx, edited.Observed.ID, edited.Revision); refused.Error == "" || refused.Observed == nil || refused.Observed.Title != renamed || refused.Observed.PrimaryConversation != nil {
		t.Fatalf("queued startup overrode an edit made while queued: %+v", refused)
	}
	canonical, err := client.CanonicalManifest(ctx, seed.Manifest.ID)
	if err != nil || canonical.Manifest.State != model.StateBacklog {
		t.Fatalf("new ticket altered existing item: %+v, %v", canonical, err)
	}
}

func TestWebProjectCatalogUsesRegistryIncludingRepositoriesWithoutItems(t *testing.T) {
	catalog := webProjects([]model.OwnedRepository{{ID: "first", Name: "renamed-project"}, {ID: "second", Name: "empty-project"}})
	if len(catalog) != 2 || catalog[0].Name != "empty-project" || catalog[1].Name != "renamed-project" {
		t.Fatalf("catalog=%+v", catalog)
	}
	if webProjects(nil) == nil {
		t.Fatal("empty catalog must be an array")
	}
}

func TestWebListReusesCLISectionsAndRepositoryIdentity(t *testing.T) {
	first := model.Repository{RootAtCreation: "/first/project", GitCommonDir: "/first/project/.git"}
	second := model.Repository{RootAtCreation: "/second/project", GitCommonDir: "/second/project/.git"}
	manifests := []model.Manifest{}
	repositories := map[string]model.Repository{}
	row := func(id, state, status string, repo model.Repository, rank int) app.WorkListItem {
		manifests = append(manifests, model.Manifest{ID: id, Repository: repo})
		repositories[id] = repo
		bucket := map[string]string{"idle": "needs_attention", "busy": "in_progress", "problem": "needs_fixing"}[status]
		return app.WorkListItem{ID: id, State: state, Repository: "project", AttentionRank: rank,
			Agent: &viewapp.AgentStatus{Status: status, Bucket: bucket}}
	}
	result := app.WorkListResult{}
	result.Sections.Active = []app.WorkListItem{
		row("later", model.StateActive, "idle", first, 2),
		row("busy", model.StateActive, "busy", first, 0),
		row("earlier", model.StateActive, "idle", second, 1),
		row("problem", model.StateActive, "problem", second, 0),
	}
	blocked := func(item app.WorkListItem) app.WorkListItem {
		item.Waiting = true
		return item
	}
	result.Sections.Waiting = []app.WorkListItem{
		blocked(row("waiting", model.StateActive, "idle", first, 0)),
		blocked(row("waiting-problem", model.StateActive, "problem", first, 0)),
	}
	result.Sections.Backlog = []app.WorkListItem{row("backlog", model.StateBacklog, "", first, 0)}
	closed := row("closed", model.StateClosed, "", second, 0)
	closed.Resolution = model.ResolutionCompleted
	result.Sections.Closed = []app.WorkListItem{closed}
	agents := map[string]string{"earlier": "idle", "later": "idle", "busy": "busy", "problem": "problem", "waiting": "idle", "waiting-problem": "problem"}
	want := workDisplaySections(result, "all", agents)
	got, err := webList(result, agents, manifests)
	if err != nil {
		t.Fatal(err)
	}
	for i, section := range got.DisplaySections {
		if section.Name != want[i].Name || len(section.Items) != len(want[i].Items) {
			t.Fatalf("section %d = %+v, want %+v", i, section, want[i])
		}
		for j, item := range section.Items {
			if item.ID != want[i].Items[j].ID || item.ProjectID != model.RepositoryKey(repositories[item.ID]) {
				t.Fatalf("row %d/%d = %+v", i, j, item)
			}
		}
	}
	if got.DisplaySections[0].Items[0].ID != "earlier" || len(got.DisplaySections[1].Items) != 2 {
		t.Fatal("attention ordering or waiting problem grouping differs from CLI")
	}
	if last := got.DisplaySections[len(got.DisplaySections)-1]; last.Name != "CLOSED" || last.Items[0].Resolution != model.ResolutionCompleted {
		t.Fatalf("closed section = %+v", last)
	}
	// Rows carry what the page uses, not volatile ages, so an unchanged list
	// encodes identically between reads.
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "last_activity_age_seconds") || strings.Contains(string(encoded), `"manifest"`) {
		t.Fatalf("list rows carry volatile or bulky fields: %s", encoded)
	}
	if _, err := webList(result, agents, nil); err == nil {
		t.Fatal("missing repository metadata silently accepted")
	}
	empty, err := webList(app.WorkListResult{}, nil, nil)
	if err != nil || empty.Projects == nil || empty.DisplaySections == nil {
		t.Fatal("empty catalog/sections should be arrays, not null")
	}
}

func TestWebNextScopesWithoutChangingCanonicalOrder(t *testing.T) {
	first := model.Repository{GitCommonDir: "/first/.git"}
	second := model.Repository{GitCommonDir: "/second/.git"}
	queue := coordinator.ActionabilityQueueResult{Candidates: []coordinator.ActionabilityQueueCandidate{
		{Manifest: model.Manifest{ID: "a1", Repository: first}, Rank: 1},
		{Manifest: model.Manifest{ID: "b1", Repository: second}, Rank: 2},
		{Manifest: model.Manifest{ID: "a2", Repository: first}, Rank: 3},
	}}
	a, b := model.RepositoryKey(first), model.RepositoryKey(second)
	for _, test := range []struct {
		name, current, want string
		projects            []string
	}{
		{"all projects by default", "a1", "b1", nil},
		{"all starts at first", "", "a1", nil},
		{"first project", "a1", "a2", []string{a}},
		{"wrap within project", "a2", "a1", []string{a}},
		{"current outside selection", "b1", "a1", []string{a}},
		{"multiple projects", "a1", "b1", []string{b, a}},
		{"cleared selection", "a1", "", []string{}},
		{"empty HTTP sentinel", "a1", "", []string{""}},
		{"unknown project", "a1", "", []string{"unknown"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := selectWebNext(queue, test.current, test.projects); got != test.want {
				t.Fatalf("next = %q, want %q", got, test.want)
			}
		})
	}
	if len(queue.Candidates) != 3 || queue.Candidates[1].Manifest.ID != "b1" {
		t.Fatal("filter mutated canonical queue")
	}
}

func TestWebSocketIsPrivateAndNeverReplacesOtherFiles(t *testing.T) {
	dir := testutil.ShortTempDir(t)
	path := filepath.Join(dir, "web", "web.sock")
	listener, err := listenWebSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode=%v err=%v", info.Mode(), err)
	}
	if _, err := listenWebSocket(path); err == nil || !strings.Contains(err.Error(), "already serving") {
		t.Fatalf("replaced a live socket: %v", err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	listener.Close()
	stale, err := listenWebSocket(path)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	stale.Close()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenWebSocket(file); err == nil {
		t.Fatal("replaced a regular file")
	}
}

func TestWebSendCarriesTheTabLeaseGeneration(t *testing.T) {
	lease := model.ControlLease{ID: "lease-7", Holder: model.LeaseHolderWeb, ClientID: "tab"}
	opts := webSendOptions(app.ResolveOptions{Selector: "auth"}, webaccess.Message{Delivery: "prompt", Text: "hi"}, "tab", lease, "rt-1", true)
	if opts.ControllerHolder != model.LeaseHolderWeb || opts.ControllerClientID != "tab" || opts.ControllerLeaseID != "lease-7" || opts.ExpectedRuntimeID != "rt-1" || !opts.RequireIdle || opts.CommandType != "prompt" {
		t.Fatalf("options = %+v", opts)
	}
}
