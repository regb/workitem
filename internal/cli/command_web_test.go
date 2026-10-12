package cli

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"reflect"
	"testing"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/config"
	"github.com/regb/workitem/internal/coordinator"
	gitpkg "github.com/regb/workitem/internal/git"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	webaccess "github.com/regb/workitem/internal/web"
)

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
	socket, err := coordinator.SocketPath(t.TempDir(), native.Root)
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
	if err != nil || len(profiles) != 2 || profiles[0].Name != "default" || profiles[1].Name != "review" || profiles[1].Label != "Pi Review" {
		t.Fatalf("agent profiles=%+v err=%v", profiles, err)
	}
	if out := backend.Create(ctx, webaccess.Create{ProjectID: "/etc", Prompt: "Do not run"}); out.Error == "" || out.Observed != nil || out.CreationUncertain {
		t.Fatalf("unknown project reached creation: %+v", out)
	}
	// No conversation backend is configured. Startup deliberately fails after
	// daemon-owned creation and the lifecycle transition have committed.
	accepted := backend.Create(ctx, webaccess.Create{ProjectID: model.RepositoryKey(seed.Manifest.Repository), Agent: "review", Prompt: "Self-contained initial request"})
	if accepted.Error != "" || accepted.Observed == nil || accepted.Observed.State != model.StateWorking {
		t.Fatalf("creation not durable before startup: %+v", accepted)
	}
	out := backend.StartCreated(ctx, accepted.Observed.ID)
	if out.Error == "" || out.Observed == nil || out.Observed.ID == seed.Manifest.ID || out.Observed.State != model.StateWorking || out.CreationUncertain {
		t.Fatalf("lost durable partial outcome: %+v", out)
	}
	shown, err := application.ShowWorkItem(ctx, app.ResolveOptions{Selector: out.Observed.ID})
	if err != nil || shown.Description != "Self-contained initial request" || shown.Manifest.Agent != "review" || shown.Manifest.Repository.GitCommonDir != seed.Manifest.Repository.GitCommonDir {
		t.Fatalf("created ticket: %+v, %v", shown, err)
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
	if _, err := coordinatorStateTransition(ctx, cfg, interrupted.Observed.ID, model.StateArchived, "work_item.archived", false); err != nil {
		t.Fatal(err)
	}
	if aborted := backend.StartCreated(ctx, interrupted.Observed.ID); aborted.Error == "" || aborted.Observed.State != model.StateArchived || aborted.Observed.PrimaryConversation != nil {
		t.Fatalf("queued startup overrode archive: %+v", aborted)
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
	project, err := webCreationProject(catalog, "first")
	if err != nil || project.ID != "first" || project.Name != "renamed-project" || project.Root != "" {
		t.Fatalf("registry project=%+v err=%v", project, err)
	}
	if _, err := webCreationProject(catalog, "forged"); err == nil {
		t.Fatal("unregistered project accepted")
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
	result.Sections.Working = []app.WorkListItem{
		row("later", model.StateWorking, "idle", first, 2),
		row("busy", model.StateWorking, "busy", first, 0),
		row("earlier", model.StateWorking, "idle", second, 1),
		row("problem", model.StateWorking, "problem", second, 0),
	}
	result.Sections.Waiting = []app.WorkListItem{
		row("waiting", model.StateWaiting, "idle", first, 0),
		row("waiting-problem", model.StateWaiting, "problem", first, 0),
	}
	result.Sections.Backlog = []app.WorkListItem{row("backlog", model.StateBacklog, "", first, 0)}
	agents := map[string]string{"earlier": "idle", "later": "idle", "busy": "busy", "problem": "problem", "waiting": "idle", "waiting-problem": "problem"}
	want := workDisplaySections(result, "active", agents)
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
	if len(got.Projects) != 2 || got.Projects[0].Name != "project" || got.Projects[1].Name != "project" || got.Projects[0].ID == got.Projects[1].ID {
		t.Fatalf("same-named repositories were conflated: %+v", got.Projects)
	}
	if !reflect.DeepEqual(result, got.WorkListResult) {
		t.Fatal("web presentation changed the source projection")
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
