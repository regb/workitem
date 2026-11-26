package daemonapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	reviewcore "github.com/regb/workitem/internal/app/core/review"
)

type failingReviewRecords struct {
	inner interface {
		LoadReview(context.Context, string) (reviewcore.State, error)
		StoreReview(context.Context, reviewcore.State, uint64, string, string, map[string]string) error
	}
}

func (f failingReviewRecords) LoadReview(ctx context.Context, item string) (reviewcore.State, error) {
	return f.inner.LoadReview(ctx, item)
}
func (f failingReviewRecords) StoreReview(context.Context, reviewcore.State, uint64, string, string, map[string]string) error {
	return errors.New("record write failed")
}

func TestReviewRecordsPublishAtomicallyWithoutGitMetadataOrNotificationOutbox(t *testing.T) {
	f, item, ws := reviewWorkspace(t)
	ctx := context.Background()
	path := filepath.Join(ws.Claim.Path, "example.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := f.Clients.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(&v1.CaptureReviewRevisionRequest{ItemId: item, CaptureId: "one", ExpectedClaimId: ws.Claim.Id, ExpectedClaimGeneration: ws.Claim.Generation}))
	if err != nil {
		t.Fatal(err)
	}
	if r.Msg.Revision.BaseCommit != r.Msg.Revision.ObservedHead || r.Msg.Revision.Id != "r1" {
		t.Fatal("first observed HEAD was not pinned as review base")
	}
	draft, err := f.Clients.Reviews.SaveReviewDraft(ctx, connect.NewRequest(&v1.SaveReviewDraftRequest{ItemId: item, OperationId: "draft", Draft: &v1.ReviewDraft{Anchor: &v1.ReviewAnchor{RevisionId: "r1", Path: "example.txt", Side: v1.ReviewSide_REVIEW_SIDE_NEW, StartLine: 1}, Body: "private reviewer comment text"}}))
	if err != nil {
		t.Fatal(err)
	}
	publish := &v1.PublishReviewRoundRequest{ItemId: item, OperationId: "publish", RevisionId: "r1", DraftIds: []string{draft.Msg.Result.DraftId}}
	if _, err := f.Clients.Reviews.PublishReviewRound(ctx, connect.NewRequest(publish)); err == nil || !strings.Contains(err.Error(), "published review round q1") {
		t.Fatalf("notification failure lost partial publication: %v", err)
	}
	state, err := f.Core().LoadReview(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Drafts) != 0 || len(state.Rounds) != 1 || len(state.Threads) != 1 || state.Threads[0].Anchor.Path != "example.txt" || state.Rounds[0].CommandID != "" || state.Rounds[0].NotifiedAt != nil {
		t.Fatal("round publication was partial or fabricated notification")
	}
	again, err := f.Clients.Reviews.PublishReviewRound(ctx, connect.NewRequest(publish))
	if err != nil || !again.Msg.Result.Reused {
		t.Fatalf("published retry sent notification again: %v %v", again, err)
	}
	if _, err := f.Clients.Reviews.NotifyReviewRound(ctx, connect.NewRequest(&v1.NotifyReviewRoundRequest{ItemId: item, RoundId: "q1"})); err == nil {
		t.Fatal("notify silently started an agent")
	}
	// Replies and a new code record are one database write. Invalid whole batches
	// fail before creating either a snapshot ref or a message.
	bad := &v1.ReplyReviewThreadsRequest{ItemId: item, OperationId: "bad", Capture: true, ExpectedClaimId: ws.Claim.Id, ExpectedClaimGeneration: ws.Claim.Generation, Replies: []*v1.ReviewReply{{ThreadId: "missing", Body: "bad"}}}
	if _, err := f.Clients.Reviews.ReplyReviewThreads(ctx, connect.NewRequest(bad)); err == nil {
		t.Fatal("invalid batch accepted")
	}
	before, _ := f.Core().LoadReview(ctx, item)
	if len(before.Revisions) != 1 || len(before.Threads[0].Messages) != 1 {
		t.Fatal("invalid batch published half")
	}
	if err := os.WriteFile(path, []byte("fixed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	good := &v1.ReplyReviewThreadsRequest{ItemId: item, OperationId: "fix", Capture: true, ExpectedClaimId: ws.Claim.Id, ExpectedClaimGeneration: ws.Claim.Generation, Replies: []*v1.ReviewReply{{ThreadId: "t1", Body: "Fixed on new revision", Addressed: true}}}
	fixed, err := f.Clients.Reviews.ReplyReviewThreads(ctx, connect.NewRequest(good))
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Msg.Result.CapturedRevision.Id != "r2" {
		t.Fatal("no reply capture")
	}
	stored, _ := f.Core().LoadReview(ctx, item)
	if stored.Threads[0].Status() != "addressed" || stored.Threads[0].Anchor.Revision != "r1" || stored.Threads[0].Messages[1].Revision != "r2" {
		t.Fatal("reply moved/resolved original anchor")
	}
	m, err := f.App().Store.LoadManifest(item)
	if err != nil {
		t.Fatal(err)
	}
	refs := integrationGit(t, m.Repository.OperationalRoot(), "for-each-ref", "--format=%(refname)", "refs/wi/review/"+item+"/")
	for _, ref := range strings.Fields(refs) {
		if !strings.Contains(ref, "/snapshots/") {
			t.Fatal("Git review metadata retained", ref)
		}
	}
	canonical, found, err := f.Core().Database().ReadReviewRecord(item)
	raw, _ := json.Marshal(canonical)
	if err != nil || !found || !strings.Contains(string(raw), "private reviewer comment text") {
		t.Fatal("comment body is not canonical coordinator content", err)
	}
	events, err := f.Core().Database().ListItemEvents(item)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(events)
	if strings.Contains(string(body), "private reviewer comment text") || strings.Contains(string(body), "Fixed on new revision") {
		t.Fatal("compact event leaked item content")
	}
	if _, err := f.Clients.Reviews.SetReviewIntent(ctx, connect.NewRequest(&v1.SetReviewIntentRequest{ItemId: item, OperationId: "approve", RevisionId: "r2", Kind: v1.ApprovalKind_APPROVAL_KIND_APPROVE})); err != nil {
		t.Fatal("unresolved concern blocked optional approval", err)
	}
}

func TestUnrecordedSnapshotRefIsHarmlessAndNextCaptureNeedsNoRecovery(t *testing.T) {
	f, item, ws := reviewWorkspace(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(ws.Claim.Path, "work.txt"), []byte("work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.App().ReviewRecords = failingReviewRecords{f.Core()}
	if _, err := f.Clients.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(&v1.CaptureReviewRevisionRequest{ItemId: item, CaptureId: "failed", ExpectedClaimId: ws.Claim.Id, ExpectedClaimGeneration: ws.Claim.Generation})); err == nil {
		t.Fatal("failed record write succeeded")
	}
	state, err := f.Core().LoadReview(ctx, item)
	if err != nil || len(state.Revisions) != 0 {
		t.Fatal("partial record created", err)
	}
	m, _ := f.App().Store.LoadManifest(item)
	if refs := integrationGit(t, m.Repository.OperationalRoot(), "for-each-ref", "--format=%(refname)", "refs/wi/review/"+item+"/snapshots/"); refs == "" {
		t.Fatal("missing harmless unrecorded ref")
	}
	f.App().ReviewRecords = f.Core()
	out, err := f.Clients.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(&v1.CaptureReviewRevisionRequest{ItemId: item, CaptureId: "next", ExpectedClaimId: ws.Claim.Id, ExpectedClaimGeneration: ws.Claim.Generation}))
	if err != nil || out.Msg.Revision.Id != "r1" {
		t.Fatalf("orphan ref blocked next normal capture: %v %v", out, err)
	}
}
