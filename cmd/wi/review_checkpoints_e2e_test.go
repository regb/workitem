package main

import (
	"connectrpc.com/connect"
	"context"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewCheckpointCLI(t *testing.T) {
	f := newBinaryFixture(t)
	ctx := context.Background()
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	created, err := f.clients.Items.CreateItem(ctx, connect.NewRequest(&v1.CreateItemRequest{RepositoryId: repo.Repository.Id, Title: "Checkpoints"}))
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
	file := filepath.Join(ws.Msg.Checkout.Claim.Path, "new.txt")
	if err := os.WriteFile(file, []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var one, two v1.CaptureReviewRevisionResponse
	f.json(&one, "review", "checkpoint", "--item", id, "--capture-id", "first")
	if err := os.WriteFile(file, []byte("second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.json(&two, "review", "checkpoint", "--item", id, "--capture-id", "second")
	if one.Revision.Id != "r1" || two.Revision.Id != "r2" {
		t.Fatal("item-scoped revisions missing")
	}
	patch := f.run("review", "diff", "r1", "r2", "--patch", "--item", id)
	if !strings.Contains(string(patch), "-first") || !strings.Contains(string(patch), "+second") {
		t.Fatal(string(patch))
	}
	var draft v1.SaveReviewDraftResponse
	f.json(&draft, "review", "comment", "--item", id, "--revision", "r2", "--path", "new.txt", "--line", "1", "-m", "Check behavior", "--operation-id", "comment")
	// No agent runs: notification is a partial outcome, never a rolled-back round.
	if out, err := f.command("review", "publish", "--item", id, "--revision", "r2", "--drafts", draft.Result.DraftId, "--operation-id", "publish"); err == nil || !strings.Contains(string(out), "published review round") {
		t.Fatalf("missing partial notification outcome: %v %s", err, out)
	}
	var threads v1.GetReviewThreadsResponse
	f.json(&threads, "review", "threads", "--item", id)
	if len(threads.Threads) != 1 || threads.Rounds[0].Id != "q1" {
		t.Fatal(threads.String())
	}
	if err := os.WriteFile(file, []byte("captured fix\n"), 0600); err != nil {
		t.Fatal(err)
	}
	combined := func() *v1.ReplyReviewThreadsResponse {
		t.Helper()
		cmd := exec.Command(f.wi, "review", "reply", "--item", id, "--capture", "--stdin", "--operation-id", "capture-reply", "--json")
		cmd.Env = f.env
		cmd.Dir = f.root
		cmd.Stdin = strings.NewReader(`{"replies":[{"thread_id":"t1","body":"Fixed","addressed":true}]}`)
		data, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		r := &v1.ReplyReviewThreadsResponse{}
		if err := protojson.Unmarshal(data, r); err != nil {
			t.Fatal(err, string(data))
		}
		return r
	}
	fixed := combined()
	if fixed.Result.CapturedRevision.Id != "r3" {
		t.Fatal(fixed.String())
	}
	again := combined()
	if !again.Result.Reused || again.Result.CapturedRevision.Commit != fixed.Result.CapturedRevision.Commit {
		t.Fatal("retry recaptured files")
	}
	f.json(&threads, "review", "thread", "t1", "--item", id)
	if threads.Threads[0].Status != v1.ThreadStatus_THREAD_STATUS_ADDRESSED {
		t.Fatal("agent resolved reviewer concern")
	}
}
