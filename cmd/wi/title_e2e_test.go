package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// TestTitleModelEndToEnd sets the title model through the CLI and checks
// items get titles from it, or their first line when the model fails.
func TestTitleModelEndToEnd(t *testing.T) {
	f := newBinaryFixture(t)
	ctx := context.Background()
	if out := string(f.run("agent", "profile", "title-model")); !strings.Contains(out, "No title model") {
		t.Fatalf("unset title model: %s", out)
	}
	if out, err := f.command("agent", "profile", "title-model", "nonexistent"); err == nil {
		t.Fatalf("unknown driver accepted: %s", out)
	}
	f.run("agent", "profile", "title-model", "pi", "model=test/model", "reasoning=low")
	var profiles v1.ListAgentProfilesResponse
	f.json(&profiles, "agent", "profile", "list")
	if m := profiles.TitleModel; m == nil || m.Driver != "pi" || m.Model != "test/model" || m.Reasoning != "low" {
		t.Fatalf("title model: %v", profiles.TitleModel)
	}

	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	var lastItem string
	titled := func(description string) string {
		t.Helper()
		created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo.Repository.Id, Description: description, GenerateTitle: true}))
		if err != nil {
			t.Fatal(err)
		}
		initial := created.Msg.Item.Title
		lastItem = created.Msg.Item.Id
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			item, err := f.clients.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: created.Msg.Item.Id}))
			if err != nil {
				t.Fatal(err)
			}
			if item.Msg.Item.Title != initial {
				// A model's title is its job's result too; a fallback's job
				// failed and has none.
				jobs, err := f.clients.Jobs.ListJobs(ctx, connect.NewRequest(&v1.ListJobsRequest{ItemId: created.Msg.Item.Id}))
				if err != nil {
					t.Fatal(err)
				}
				for _, job := range jobs.Msg.Jobs {
					if job.Kind == "suggest_title" && job.GetTitle() != nil && job.GetTitle().Title != item.Msg.Item.Title {
						t.Fatalf("title result %q, item %q", job.GetTitle().Title, item.Msg.Item.Title)
					}
				}
				return item.Msg.Item.Title
			}
			if time.Now().After(deadline) {
				t.Fatalf("no title for %q", description)
			}
		}
	}
	if title := titled("write the docs"); title != "Test suggested title" {
		t.Fatalf("model title: %q", title)
	}
	jobs, err := f.clients.Jobs.ListJobs(ctx, connect.NewRequest(&v1.ListJobsRequest{ItemId: lastItem}))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs.Msg.Jobs) == 0 || jobs.Msg.Jobs[0].GetTitle().GetTitle() != "Test suggested title" {
		t.Fatalf("title job result: %v", jobs.Msg.Jobs)
	}
	if title := titled("Repair the importer\nthen fail the model"); title != "Repair the importer" {
		t.Fatalf("fallback title: %q", title)
	}

	f.run("agent", "profile", "title-model", "--clear")
	f.json(&profiles, "agent", "profile", "list")
	if profiles.TitleModel != nil {
		t.Fatalf("title model not cleared: %v", profiles.TitleModel)
	}
}
