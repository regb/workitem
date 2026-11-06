package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
	web "github.com/regb/workitem/internal/web"
)

func TestWebReadsAreGeneratedAPITranslations(t *testing.T) {
	f := newClientFixture(t)
	b := webBackend{f.cfg}
	item := f.create(t, "Browser item")
	ctx := context.Background()
	if err := b.CheckItem(ctx, item.Id); err != nil {
		t.Fatal(err)
	}
	if err := b.CheckItem(ctx, "01KZYHGDCECSFS4BJ2SNTQP49W"); err != web.ErrNotFound {
		t.Fatalf("exact ID check: %v", err)
	}
	projects, err := b.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].ID != f.repo {
		t.Fatalf("projects: %v %v", projects, err)
	}
	list, err := b.List(ctx, false)
	if err != nil || len(list.DisplaySections) != 1 || list.DisplaySections[0].Name != "BACKLOG" || list.DisplaySections[0].Items[0].ID != item.Id {
		t.Fatalf("list: %v %v", list, err)
	}
	chat, err := b.Chat(ctx, item.Id)
	if err != nil || chat.Item.ID != item.Id || chat.Project.ID != f.repo || len(chat.History.Messages) != 0 {
		t.Fatalf("chat: %+v %v", chat, err)
	}
	if out := b.AddNote(ctx, item.Id, web.NoteRequest{Text: "browser note"}); out.Error != "" {
		t.Fatal(out)
	}
	notes, err := b.Notes(ctx, item.Id)
	if err != nil || len(notes.Notes) != 1 || notes.Notes[0].Text != "browser note" {
		t.Fatalf("notes: %+v %v", notes, err)
	}
	if out := b.Attach(ctx, item.Id, web.AttachRequest{Name: "evidence.txt", Content: []byte("evidence")}); out.Error != "" {
		t.Fatal(out)
	}
	notes, _ = b.Notes(ctx, item.Id)
	reader, meta, err := b.Attachment(ctx, item.Id, notes.Attachments[0].SHA256)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if meta.Name != "evidence.txt" {
		t.Fatal(meta)
	}
	search, err := b.Search(ctx, "browser note", "")
	if err != nil || search.Total != 1 {
		t.Fatalf("search: %v %v", search, err)
	}
	settings, err := b.GeneralSettings(ctx)
	if err != nil || settings.Version == "" || settings.DefaultAgent != "test" || settings.DataRoot == "" {
		t.Fatalf("settings: %v %v", settings, err)
	}
	profiles, err := b.AgentProfiles(ctx)
	if err != nil || len(profiles.Profiles) != 1 {
		t.Fatalf("profiles: %v %v", profiles, err)
	}
}
func TestWebRepositorySettingsAndFileMutationsUseAPI(t *testing.T) {
	f := newClientFixture(t)
	b := webBackend{f.cfg}
	ctx := context.Background()
	setup, err := b.RepositorySetup(ctx, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	setup.Run = "true"
	setup.Trust = "always"
	setup.Skip = []string{".private"}
	out := b.SetRepositorySetup(ctx, f.repo, setup)
	if out.Error != "" {
		t.Fatal(out)
	}
	saved, err := b.RepositorySetup(ctx, f.repo)
	if err != nil || saved.Run != "true" || saved.Trust != "always" || len(saved.Skip) != 1 {
		t.Fatalf("setup: %v %v", saved, err)
	}
	if stale := b.SetRepositorySetup(ctx, f.repo, setup); stale.Error == "" {
		t.Fatal("stale setup revision accepted")
	}
	if out := b.AddRepositoryFile(ctx, f.repo, web.RepositoryFileRequest{Path: ".env", Content: []byte("PORT=8080")}); out.Error != "" {
		t.Fatal(out)
	}
	repositories, err := b.Repositories(ctx)
	if err != nil || len(repositories) != 1 || len(repositories[0].Files) != 1 {
		t.Fatalf("repositories: %v %v", repositories, err)
	}
	if out := b.RemoveRepositoryFile(ctx, f.repo, web.RepositoryFileRequest{Path: ".env"}); out.Error != "" {
		t.Fatal(out)
	}
	env, err := b.Environment(ctx, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	request := web.EnvironmentRequest{Revision: env.Revision}
	request.Vars = append(request.Vars, model.EnvVar{Key: "PORT", Value: "8080"})
	if out := b.SetEnvironment(ctx, f.repo, request); out.Error != "" {
		t.Fatal(out)
	}
	savedEnv, err := b.Environment(ctx, f.repo)
	if err != nil || len(savedEnv.Vars) != 1 {
		t.Fatalf("environment: %v %v", savedEnv, err)
	}
}
func TestWebLifecycleFailureReadsRetainedDurableState(t *testing.T) {
	f := newClientFixture(t)
	b := webBackend{f.cfg}
	ctx := context.Background()
	created := b.Create(ctx, web.Create{ProjectID: f.repo, Prompt: "Task description"})
	if created.Error != "" || created.Observed == nil {
		t.Fatalf("creation: %+v", created)
	}
	out := b.StartCreated(ctx, created.Observed.ID, created.Revision)
	if out.Error == "" || out.Observed == nil || out.Observed.State != "active" {
		t.Fatalf("partial startup: %+v", out)
	}
	shown, err := f.api.Clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: created.Observed.ID, IncludeDescription: true}))
	if err != nil || shown.Msg.Description != "Task description" {
		t.Fatalf("description lost: %v %v", shown, err)
	}
	if out := b.Close(ctx, created.Observed.ID, web.CloseRequest{Resolution: "abandoned"}); out.Error != "" {
		t.Fatal(out)
	}
}
func TestWebSocketIsPrivateAndNeverReplacesOtherFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "web.sock")
	listener, err := listenWebSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if _, err := listenWebSocket(path); err == nil {
		t.Fatal("replaced live socket")
	}
	listener.Close()
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenWebSocket(path); err == nil {
		t.Fatal("replaced regular file")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "retain" {
		t.Fatal(data)
	}
}
func TestWebListAPIShapeKeepsBrowserFields(t *testing.T) {
	f := newClientFixture(t)
	f.create(t, "Snapshot")
	value, err := (webBackend{f.cfg}).List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	if shape["display_sections"] == nil || shape["projects"] == nil {
		t.Fatal(string(raw))
	}
}
