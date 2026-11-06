package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitest"
	"github.com/regb/workitem/internal/apitransport"
	"github.com/regb/workitem/internal/config"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type clientFixture struct {
	api       *apitest.Fixture
	cfg       Config
	out, errs *bytes.Buffer
	repo      string
}

func newClientFixture(t *testing.T) *clientFixture {
	t.Helper()
	api := apitest.New(t)
	repo := api.Repository(t, "test")
	out, errs := new(bytes.Buffer), new(bytes.Buffer)
	cfg := config.Default()
	return &clientFixture{api: api, repo: repo, out: out, errs: errs, cfg: Config{API: api.Clients, SocketPath: api.Socket, CWD: api.Root, Env: map[string]string{}, GlobalConfig: &cfg, Stdout: out, Stderr: errs, Stdin: strings.NewReader("")}}
}
func (f *clientFixture) run(t *testing.T, args ...string) string {
	t.Helper()
	f.out.Reset()
	f.errs.Reset()
	if code := Run(context.Background(), args, f.cfg); code != ExitOK {
		t.Fatalf("wi %v: code=%d stderr=%s stdout=%s", args, code, f.errs, f.out)
	}
	return f.out.String()
}
func (f *clientFixture) decode(t *testing.T, out string, msg proto.Message) {
	t.Helper()
	if err := protojson.Unmarshal([]byte(out), msg); err != nil {
		t.Fatalf("API JSON: %v\n%s", err, out)
	}
}
func (f *clientFixture) create(t *testing.T, title string) *v1.Item {
	t.Helper()
	var v v1.CreateItemResponse
	f.decode(t, f.run(t, "new", "--repo", "test", "--title", title, "--json"), &v)
	return v.Item
}
func TestClientCRUDAndAPIJSON(t *testing.T) {
	f := newClientFixture(t)
	item := f.create(t, "Remote item")
	if item.Id == "" || item.Revision != 1 || item.State != v1.ItemState_ITEM_STATE_BACKLOG {
		t.Fatal(item)
	}
	if strings.Contains(f.out.String(), "work_item_id") || !strings.Contains(f.out.String(), "agent_profile") {
		t.Fatal("CLI did not print API JSON", f.out)
	}
	var shown v1.GetItemResponse
	f.decode(t, f.run(t, "show", item.Slug, "--json"), &shown)
	if shown.Item.Id != item.Id {
		t.Fatal(shown.Item)
	}
	var updated v1.UpdateItemResponse
	f.decode(t, f.run(t, "edit", item.Id, "--title", "Clarified", "--label", "backend", "--deep", "--json"), &updated)
	if updated.Item.Title != "Clarified" || !updated.Item.DeepWork || len(updated.Item.Labels) != 1 || updated.Item.Checkout.Branch != item.Checkout.Branch {
		t.Fatal(updated.Item)
	}
	f.run(t, "edit", item.Id, "--deep=false", "--remove-label", "backend", "--json")
	f.run(t, "state", "set", "active", "--item", item.Id, "--json")
	var workspace v1.GetWorkspaceResponse
	f.decode(t, f.run(t, "workspace", "status", item.Id, "--json"), &workspace)
	if workspace.Checkout.Claim != nil {
		t.Fatal("state primitive allocated a workspace")
	}
	f.run(t, "workspace", "ensure", item.Id, "--json")
	f.decode(t, f.run(t, "workspace", "status", item.Id, "--json"), &workspace)
	if workspace.Checkout.Claim == nil || workspace.Setup.State != v1.SetupState_SETUP_STATE_SUCCEEDED {
		t.Fatal(workspace.String())
	}
	f.run(t, "workspace", "setup", item.Id, "--json")
	f.run(t, "workspace", "release", item.Id, "--json")
	f.run(t, "close", item.Id, "--resolution", "completed", "--json")
	f.decode(t, f.run(t, "show", item.Id, "--json"), &shown)
	if shown.Item.State != v1.ItemState_ITEM_STATE_CLOSED || shown.Item.Slug != "" {
		t.Fatal(shown.Item)
	}
	f.run(t, "reopen", item.Id, "--backlog", "--json")
	f.run(t, "close", item.Id, "--resolution", "abandoned", "--json")
	f.run(t, "delete", "--yes", item.Id, "--json")
	f.errs.Reset()
	if code := Run(context.Background(), []string{"show", item.Id, "--json"}, f.cfg); code != ExitNotFound {
		t.Fatalf("missing item code=%d: %s", code, f.errs)
	}
}
func TestClientLabelsMergeOnlyClientDefaults(t *testing.T) {
	f := newClientFixture(t)
	f.cfg.GlobalConfig.Item.Defaults.Labels = []string{"global", "shared"}
	f.cfg.Env["WI_ITEM_DEFAULT_LABELS"] = "environment,shared"
	var created v1.CreateItemResponse
	f.decode(t, f.run(t, "new", "--repo", "test", "--title", "Labels", "--label", "explicit", "--json"), &created)
	slices.Sort(created.Item.Labels)
	if strings.Join(created.Item.Labels, ",") != "environment,explicit,global,shared" {
		t.Fatal(created.Item.Labels)
	}
	f.decode(t, f.run(t, "new", "--repo", "test", "--title", "No defaults", "--no-default-labels", "--label", "only", "--json"), &created)
	if strings.Join(created.Item.Labels, ",") != "only" {
		t.Fatal(created.Item.Labels)
	}
	f.cfg.GlobalConfig.List.Labels = []string{"+global"}
	f.cfg.Env["WI_LIST_LABELS"] = "+environment"
	var listed v1.ListItemsResponse
	f.decode(t, f.run(t, "list", "--label", "!,+only", "--json"), &listed)
	if len(listed.Items) != 1 || listed.Items[0].Title != "No defaults" {
		t.Fatal(listed.String())
	}
}
func TestClientNotesAttachmentsAndSearch(t *testing.T) {
	f := newClientFixture(t)
	item := f.create(t, "History")
	f.cfg.Env["WI_ID"] = item.Id
	f.run(t, "note", "remember this phrase", "--json")
	path := filepath.Join(f.api.Root, "evidence.txt")
	if err := os.WriteFile(path, []byte("binary evidence\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t, "attach", path, "--as", "record.txt", "--json")
	var notes v1.ListNotesResponse
	f.decode(t, f.run(t, "notes", "--json"), &notes)
	if len(notes.Notes) != 2 || len(notes.Attachments) != 1 {
		t.Fatal(notes.String())
	}
	if out := f.run(t, "attachment", "cat", "record.txt"); out != "binary evidence\x00" {
		t.Fatalf("content %q", out)
	}
	var hits v1.SearchItemsResponse
	f.decode(t, f.run(t, "search", "remember phrase", "--json"), &hits)
	if hits.Total != 1 || hits.Hits[0].Field != "notes" {
		t.Fatal(hits.String())
	}
	description := filepath.Join(f.api.Root, "description.md")
	_ = os.WriteFile(description, []byte("description needle"), 0600)
	f.run(t, "edit", "--desc-file", description, "--json")
	f.decode(t, f.run(t, "search", "needle", "--json"), &hits)
	if hits.Total != 1 || hits.Hits[0].Field != "description" {
		t.Fatal(hits.String())
	}
	f.run(t, "events", "--json")
}
func TestAttachUsesTheCheckoutRelativePath(t *testing.T) {
	f := newClientFixture(t)
	item := f.create(t, "Evidence path")
	f.run(t, "state", "set", "active", item.Id)
	var ensured v1.EnsureWorkspaceResponse
	f.decode(t, f.run(t, "workspace", "ensure", item.Id, "--json"), &ensured)
	root := ensured.Checkout.Claim.Path
	file := filepath.Join(root, "nested", "proof.txt")
	_ = os.MkdirAll(filepath.Dir(file), 0700)
	_ = os.WriteFile(file, []byte("proof"), 0600)
	f.cfg.CWD = root
	f.cfg.Env["WI_ID"] = item.Id
	var attached v1.AttachResponse
	f.decode(t, f.run(t, "attach", "nested/proof.txt", "--json"), &attached)
	if attached.Note.Attachment.Path != "nested/proof.txt" {
		t.Fatal(attached.String())
	}
	f.run(t, "workspace", "release", item.Id, "--json")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("unchanged attached copy still present: %v", err)
	}
}
func TestClientBlockersAndAttentionUseRemoteState(t *testing.T) {
	f := newClientFixture(t)
	item := f.create(t, "Waiting")
	f.cfg.Env["WI_ID"] = item.Id
	f.run(t, "state", "set", "active", "--json")
	f.run(t, "wait", "--for", "security review", "--label", "review", "--meta", "source=test", "--json")
	var shown v1.GetItemResponse
	f.decode(t, f.run(t, "show", "--json"), &shown)
	if shown.Item.State != v1.ItemState_ITEM_STATE_ACTIVE || len(shown.Item.Blockers) != 1 || shown.Item.Blockers[0].Meta["source"] != "test" {
		t.Fatal(shown.String())
	}
	f.run(t, "wait", "--clear", "--json")
	f.run(t, "wait", "--until", "2h", "--json")
	f.run(t, "blocker", "list", "--json")
	f.run(t, "blocker", "clear", "--json")
	f.run(t, "attention", "defer", "--json")
	f.decode(t, f.run(t, "attention", "activity", "--json"), &shown)
	if shown.Observation.Activity.LastDeferredAt == nil {
		t.Fatal(shown.String())
	}
	f.run(t, "attention", "queue", "--json")
}
func TestClientValidationHasNoDurableEffects(t *testing.T) {
	f := newClientFixture(t)
	item := f.create(t, "Existing")
	f.cfg.Env["WI_ID"] = item.Id
	cases := [][]string{{"new"}, {"new", "--title", ""}, {"new", "--title", "x", "--untitled"}, {"new", "--title", "x", "--home"}, {"new", "--repo", "test", "--title", "x", "--label", "bad!label"}, {"start", "--new", "x"}, {"start", "--home", item.Id}, {"workspace", "release", "--force", item.Id}, {"edit", "--item", item.Id, item.Id}, {"state", "set", "unknown", item.Id}, {"list", "--not-a-flag"}, {"title", "no"}, {"label", "x"}, {"deep"}}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f.out.Reset()
			f.errs.Reset()
			if code := Run(context.Background(), append(args, "--json"), f.cfg); code != ExitUsage {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, f.out, f.errs)
			}
			if f.out.Len() != 0 {
				t.Fatal("rejected mutation printed success", f.out)
			}
		})
	}
	listed, err := f.api.Clients.Items.ListItems(context.Background(), connect.NewRequest(&v1.ListItemsRequest{}))
	if err != nil || len(listed.Msg.Items) != 1 {
		t.Fatalf("invalid input mutated state: %v %v", listed, err)
	}
}
func TestRestrictedClientCannotMutateLifecycleOrSettings(t *testing.T) {
	f := newClientFixture(t)
	item := f.create(t, "Restricted")
	client, err := apitransport.NewClients(f.api.AgentSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	f.cfg.API = client
	f.cfg.Env["WI_ID"] = item.Id
	for _, args := range [][]string{{"state", "set", "active"}, {"edit", "--title", "Forbidden"}, {"workspace", "ensure"}, {"new", "--repo", "test", "--title", "Cannot start", "--start"}} {
		f.out.Reset()
		f.errs.Reset()
		if code := Run(context.Background(), args, f.cfg); code == ExitOK {
			t.Fatal("restricted operation succeeded", args)
		}
	}
	var created v1.CreateItemResponse
	f.decode(t, f.run(t, "new", "--repo", "test", "--title", "Agent backlog", "--json"), &created)
	if created.Item.State != v1.ItemState_ITEM_STATE_BACKLOG || created.Item.Checkout.Claim != nil {
		t.Fatal(created.String())
	}
}
func TestRepositorySettingsFilesAndEnvironmentThroughAPI(t *testing.T) {
	f := newClientFixture(t)
	f.cfg.Env["WI_ID"] = f.create(t, "Context").Id
	f.run(t, "repo", "list", "--json")
	f.run(t, "repo", "settings", "set", "plugins.setup.trust", "always", "--json")
	f.run(t, "repo", "settings", "show", "--json")
	f.run(t, "repo", "settings", "unset", "plugins.setup.trust", "--json")
	f.run(t, "repo", "env", "set", "PORT=8080", "TOKEN=private", "--json")
	var environment v1.GetEnvironmentResponse
	f.decode(t, f.run(t, "repo", "env", "show", "--json"), &environment)
	if len(environment.Variables) != 2 {
		t.Fatal(environment.String())
	}
	f.run(t, "repo", "env", "unset", "TOKEN", "--json")
	path := filepath.Join(f.api.Root, "local.env")
	_ = os.WriteFile(path, []byte("PORT=8080"), 0600)
	f.run(t, "repo", "files", "add", path, "--as", ".env", "--json")
	if out := f.run(t, "repo", "files", "cat", ".env"); out != "PORT=8080" {
		t.Fatal(out)
	}
	f.run(t, "repo", "files", "remove", ".env", "--json")
	f.run(t, "repo", "rename", "test", "renamed", "--json")
	f.run(t, "repo", "archive", "renamed", "--json")
	f.run(t, "repo", "list", "--all", "--json")
	f.run(t, "repo", "restore", "renamed", "--json")
}
func TestProfilesAndSystemThroughAPI(t *testing.T) {
	f := newClientFixture(t)
	f.run(t, "agent", "profile", "add", "other", "--driver", "pi", "--model", "test/model", "--json")
	f.run(t, "agent", "profile", "default", "other", "--json")
	f.run(t, "agent", "profile", "default", "test", "--json")
	f.run(t, "agent", "profile", "remove", "other", "--json")
	f.run(t, "agent", "profile", "drivers", "--json")
	f.run(t, "version", "--json")
	f.run(t, "info", "--json")
	var cfg v1.GetConfigResponse
	f.decode(t, f.run(t, "config", "--json"), &cfg)
	var tree map[string]any
	if err := json.Unmarshal([]byte(cfg.ConfigJson), &tree); err != nil {
		t.Fatal(err)
	}
	if tree["drivers"] == nil {
		t.Fatal(tree)
	}
	f.run(t, "plugin", "list", "--json")
}
func TestFailedStartRetainsDescriptionAndReportsAJob(t *testing.T) {
	f := newClientFixture(t)
	file := filepath.Join(f.api.Root, "task.md")
	_ = os.WriteFile(file, []byte("durable description"), 0600)
	f.out.Reset()
	f.errs.Reset()
	if code := Run(context.Background(), []string{"new", "--repo", "test", "--title", "Partial", "--start", "--agent-mode", "headless", "--prompt", "separate prompt", "--desc-file", file, "--json"}, f.cfg); code == ExitOK {
		t.Fatal("unconfigured test runtime unexpectedly started")
	}
	items, err := f.api.Clients.Items.ListItems(context.Background(), connect.NewRequest(&v1.ListItemsRequest{}))
	if err != nil || len(items.Msg.Items) != 1 {
		t.Fatalf("created item lost: %v %v", items, err)
	}
	value, err := f.api.Clients.Items.GetItem(context.Background(), connect.NewRequest(&v1.GetItemRequest{ItemId: items.Msg.Items[0].Id, IncludeDescription: true}))
	if err != nil || value.Msg.Description != "durable description" || value.Msg.Item.State != v1.ItemState_ITEM_STATE_ACTIVE {
		t.Fatalf("partial create: %v %v", value, err)
	}
	jobs, err := f.api.Clients.Jobs.ListJobs(context.Background(), connect.NewRequest(&v1.ListJobsRequest{ItemId: value.Msg.Item.Id}))
	if err != nil || len(jobs.Msg.Jobs) == 0 || jobs.Msg.Jobs[0].Partial == nil {
		t.Fatalf("partial job: %v %v", jobs, err)
	}
}
