package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func TestOrdinaryMergeCLIRecordsSquashAndPreservedSeries(t *testing.T) {
	f := newBinaryFixture(t)
	ctx := context.Background()
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo.Repository.Id, Title: "Default squash title"}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.Item.Id
	if _, err := f.clients.Items.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: id, State: v1.ItemState_ITEM_STATE_ACTIVE})); err != nil {
		t.Fatal(err)
	}
	ws, err := f.clients.Workspaces.EnsureWorkspace(ctx, connect.NewRequest(&v1.EnsureWorkspaceRequest{ItemId: id, SkipSetup: true}))
	if err != nil {
		t.Fatal(err)
	}
	path := ws.Msg.Checkout.Claim.Path
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		cmd.Env = f.env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("config", "user.name", "Merge test")
	git("config", "user.email", "merge@example.invalid")
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(path, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := git("rev-parse", "main")
	write("work.txt", "uncommitted merge input\n")
	var input v1.CaptureReviewRevisionResponse
	f.json(&input, "review", "checkpoint", "--item", id, "--capture-id", "feedback")
	var draft v1.SaveReviewDraftResponse
	f.json(&draft, "review", "comment", "--item", id, "--revision", input.Revision.Id, "-m", "An unresolved concern", "--operation-id", "draft")
	_, _ = f.command("review", "publish", "--item", id, "--revision", input.Revision.Id, "--drafts", draft.Result.DraftId, "--operation-id", "publish")
	var result v1.IntegrateItemResponse
	f.json(&result, "merge", "--item", id, "--local", "--message", "Reviewed squash")
	if result.Job.State != v1.JobState_JOB_STATE_SUCCEEDED || len(result.Job.Warnings) == 0 {
		t.Fatal("unresolved feedback blocked merge or warning lost", result.String())
	}
	after := git("rev-parse", "main")
	if merged := result.Job.GetIntegration(); merged.GetCommit() != after || merged.GetTargetBranch() != "main" || merged.GetRevisionId() == "" {
		t.Fatal("merge result", result.Job.String())
	}
	if git("rev-parse", "HEAD") != after || git("rev-parse", after+"^") != before || git("log", "-1", "--format=%s", after) != "Reviewed squash" {
		t.Fatal("wrong squash or continued source")
	}
	records, err := f.clients.Reviews.ListReviewRevisions(ctx, connect.NewRequest(&v1.ListReviewRevisionsRequest{ItemId: id}))
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Msg.Merges) != 1 || records.Msg.Merges[0].Commit != after || records.Msg.Merges[0].Before != before || records.Msg.Merges[0].Tree != git("rev-parse", "HEAD^{tree}") {
		t.Fatal("missing canonical result evidence", records.Msg)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		write(name, name)
		git("add", name)
		git("commit", "-m", name)
	}
	tip := git("rev-parse", "HEAD")
	f.json(&result, "merge", "--item", id, "main", "--local", "--rebase")
	if result.Job.GetIntegration().GetCommit() != tip {
		t.Fatal("rebase result", result.Job.String())
	}
	if git("rev-parse", "main") != tip || git("rev-list", "--count", after+"..main") != "2" {
		t.Fatal("--rebase did not preserve ordinary series")
	}
	f.json(&result, "merge", "--item", id, "--local")
	if git("rev-parse", "main") != tip {
		t.Fatal("already-contained work created empty commit")
	}
	records, err = f.clients.Reviews.ListReviewRevisions(ctx, connect.NewRequest(&v1.ListReviewRevisionsRequest{ItemId: id}))
	if err != nil || len(records.Msg.Merges) != 3 {
		t.Fatal("merge history not retained", err)
	}
}
