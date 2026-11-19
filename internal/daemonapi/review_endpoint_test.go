package daemonapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apitransport"
)

func TestReviewUsesOperatorAuthorityWithoutBypassingRestrictedEndpoint(t *testing.T) {
	f, item, ws := reviewWorkspace(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(ws.Claim.Path, "review.txt"), []byte("code\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Ordinary operator clients need no review bootstrap or additional authority.
	captured, err := f.clients.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(&v1.CaptureReviewRevisionRequest{ItemId: item, CaptureId: "operator", ExpectedClaimId: ws.Claim.Id, ExpectedClaimGeneration: ws.Claim.Generation}))
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := f.clients.Reviews.ListReviewRevisions(ctx, connect.NewRequest(&v1.ListReviewRevisionsRequest{ItemId: item}))
	if err != nil || len(revisions.Msg.Revisions) != 1 {
		t.Fatal("ordinary operator review failed", err)
	}
	client, err := apitransport.NewClients(filepath.Join(f.root, "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cases := []struct {
		name string
		run  func() error
	}{
		{"revisions", func() error {
			r := connect.NewRequest(&v1.ListReviewRevisionsRequest{ItemId: item})
			r.Header().Set("Wi-Client-Kind", "operator")
			r.Header().Set("Wi-Control-Actor", "operator")
			_, err := client.Reviews.ListReviewRevisions(ctx, r)
			return err
		}},
		{"immutable diff", func() error {
			_, err := client.Reviews.GetRevisionReview(ctx, connect.NewRequest(&v1.GetRevisionReviewRequest{ItemId: item, RevisionId: captured.Msg.Revision.Id}))
			return err
		}},
		{"threads", func() error {
			_, err := client.Reviews.GetReviewThreads(ctx, connect.NewRequest(&v1.GetReviewThreadsRequest{ItemId: item}))
			return err
		}},
		{"capture", func() error {
			_, err := client.Reviews.CaptureReviewRevision(ctx, connect.NewRequest(&v1.CaptureReviewRevisionRequest{ItemId: item, CaptureId: "sandbox", ExpectedClaimId: ws.Claim.Id, ExpectedClaimGeneration: ws.Claim.Generation}))
			return err
		}},
		{"reply", func() error {
			_, err := client.Reviews.ReplyReviewThreads(ctx, connect.NewRequest(&v1.ReplyReviewThreadsRequest{ItemId: item, OperationId: "sandbox", RevisionId: captured.Msg.Revision.Id}))
			return err
		}},
		{"draft", func() error {
			_, err := client.Reviews.SaveReviewDraft(ctx, connect.NewRequest(&v1.SaveReviewDraftRequest{ItemId: item, OperationId: "sandbox", Draft: &v1.ReviewDraft{Anchor: &v1.ReviewAnchor{RevisionId: captured.Msg.Revision.Id}, Body: "not allowed"}}))
			return err
		}},
		{"approval", func() error {
			_, err := client.Reviews.SetReviewIntent(ctx, connect.NewRequest(&v1.SetReviewIntentRequest{ItemId: item, OperationId: "sandbox", RevisionId: captured.Msg.Revision.Id, Kind: "approve"}))
			return err
		}},
		{"notification", func() error {
			_, err := client.Reviews.NotifyReviewRound(ctx, connect.NewRequest(&v1.NotifyReviewRoundRequest{ItemId: item, RoundId: "q1"}))
			return err
		}},
		{"workspace", func() error {
			_, err := client.Workspaces.GetWorkspace(ctx, connect.NewRequest(&v1.GetWorkspaceRequest{ItemId: item}))
			return err
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Fatalf("restricted endpoint bypass: %v", err)
			}
		})
	}
	state, err := f.core.LoadReview(ctx, item)
	if err != nil || len(state.Revisions) != 1 || len(state.Drafts) != 0 || len(state.Approvals) != 0 || len(state.Threads) != 0 {
		t.Fatal("denied requests changed review content", err)
	}
	// The safe restricted-agent operations are not affected by review cleanup.
	info, err := client.System.GetInfo(ctx, connect.NewRequest(&v1.GetInfoRequest{}))
	if err != nil || info.Msg.Caller.GetAccess() != v1.CallerAccess_CALLER_ACCESS_AGENT {
		t.Fatal("endpoint identity changed", err)
	}
	shown, err := client.Items.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: item}))
	if err != nil || shown.Msg.Item.Checkout.Claim != nil {
		t.Fatal("safe item read leaked a checkout claim", err)
	}
}
