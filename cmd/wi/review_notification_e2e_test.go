package main

import (
	"connectrpc.com/connect"
	"context"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewNotificationsAreOneShotPromptsOrBusyFollowUps(t *testing.T) {
	f := newBinaryFixture(t)
	ctx := context.Background()
	var repo v1.AddRepositoryResponse
	f.json(&repo, "repo", "add", "test", "--from", f.source, "--local")
	for _, busy := range []bool{false, true} {
		prompt := "initial prompt"
		title := "Idle review"
		if busy {
			prompt = "hold busy agent"
			title = "Busy review"
		}
		var started v1.CreateAndStartItemResponse
		f.json(&started, "new", "--repo", repo.Repository.Id, "--title", title, "--start", "--agent-mode", "headless", "--prompt", prompt)
		item := started.Item.Id
		if busy {
			f.historyReady(item, 1)
		} else {
			f.historyReady(item, 2)
			f.idle(item)
		}
		ws, err := f.clients.Workspaces.GetWorkspace(ctx, connect.NewRequest(&v1.GetWorkspaceRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws.Msg.Checkout.Claim.Path, "review.txt"), []byte("code\n"), 0600); err != nil {
			t.Fatal(err)
		}
		var captured v1.CaptureReviewRevisionResponse
		f.json(&captured, "review", "checkpoint", "--item", item, "--capture-id", "snapshot")
		var draft v1.SaveReviewDraftResponse
		f.json(&draft, "review", "comment", "--item", item, "--revision", captured.Revision.Id, "-m", "private concern not copied to chat", "--operation-id", "draft")
		publish := []string{"review", "publish", "--item", item, "--revision", captured.Revision.Id, "--drafts", draft.Result.DraftId, "--operation-id", "publish"}
		var round v1.PublishReviewRoundResponse
		f.json(&round, publish...)
		if round.Result.RoundId != "q1" {
			t.Fatal(round.String())
		}
		f.idle(item)
		expected := 4
		if busy {
			expected = 3
		}
		f.historyReady(item, expected)
		threads, err := f.clients.Reviews.GetReviewThreads(ctx, connect.NewRequest(&v1.GetReviewThreadsRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		q := threads.Msg.Rounds[0]
		if q.NotifiedAt == nil || q.CommandId == "" {
			t.Fatal("successful notification receipt absent", q)
		}
		history, err := f.clients.Agents.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: item}))
		if err != nil {
			t.Fatal(err)
		}
		count := len(history.Msg.Messages)
		body := history.Msg.String()
		if !strings.Contains(body, "round q1") || strings.Contains(body, "private concern not copied to chat") {
			t.Fatal("notification copied comment bodies or omitted round identity")
		}
		f.json(&round, publish...)
		if !round.Result.Reused {
			t.Fatal("publication retry not reused")
		}
		time.Sleep(100 * time.Millisecond)
		history, err = f.clients.Agents.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: item}))
		if err != nil || len(history.Msg.Messages) != count {
			t.Fatal("publication retry resent notification", err)
		}
		var notified v1.NotifyReviewRoundResponse
		f.json(&notified, "review", "notify", "q1", "--item", item, "--operation-id", "explicit-resend")
		if notified.Round.CommandId == q.CommandId {
			t.Fatal("explicit resend did not create a new ordinary command")
		}
		f.historyReady(item, count+2)
		f.idle(item)
	}
}
