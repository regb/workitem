package daemonapi

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/app"
	reviewcore "github.com/regb/workitem/internal/app/core/review"
)

func reviewAnchor(a reviewcore.Anchor) *v1.ReviewAnchor {
	return &v1.ReviewAnchor{RevisionId: a.Revision, FromRevisionId: a.FromRevision, Path: a.Path, Side: a.Side, StartLine: a.StartLine, EndLine: a.EndLine, BlobSha256: a.BlobSHA256}
}
func coreAnchor(a *v1.ReviewAnchor) reviewcore.Anchor {
	if a == nil {
		return reviewcore.Anchor{}
	}
	return reviewcore.Anchor{Revision: a.RevisionId, FromRevision: a.FromRevisionId, Path: a.Path, Side: a.Side, StartLine: a.StartLine, EndLine: a.EndLine, BlobSHA256: a.BlobSha256}
}
func reviewDraft(d reviewcore.Draft) *v1.ReviewDraft {
	return &v1.ReviewDraft{Id: d.ID, ThreadId: d.Thread, Anchor: reviewAnchor(d.Anchor), Body: d.Body, Resolution: d.Resolution}
}
func reviewRound(q reviewcore.Round) *v1.ReviewRound {
	out := &v1.ReviewRound{Id: q.ID, RevisionId: q.Revision, FromRevisionId: q.FromRevision, MessageIds: q.Messages, CreatedAt: stamp(q.CreatedAt), CommandId: q.CommandID}
	if q.NotifiedAt != nil {
		out.NotifiedAt = stamp(*q.NotifiedAt)
	}
	return out
}
func reviewMutation(v app.ReviewMutationResult) *v1.ReviewMutation {
	out := &v1.ReviewMutation{RoundId: v.Receipt.Round, DraftId: v.Receipt.Draft, ApprovalId: v.Receipt.Approval, ThreadIds: v.Receipt.Threads, Version: uint32(v.State.Version), Reused: v.Reused}
	if v.Captured != nil {
		out.CapturedRevision = codeRevision(*v.Captured)
	}
	return out
}
func (s *Server) GetReviewThreads(ctx context.Context, req *connect.Request[v1.GetReviewThreadsRequest]) (*connect.Response[v1.GetReviewThreadsResponse], error) {
	state, err := s.App.ReviewRecordsFor(ctx, resolve(req.Msg.ItemId))
	if err != nil {
		return nil, checkpointError(err)
	}
	out := &v1.GetReviewThreadsResponse{Version: uint32(state.Version)}
	for _, t := range state.Threads {
		thread := &v1.ReviewThread{Id: t.ID, Anchor: reviewAnchor(t.Anchor), Status: t.Status()}
		for _, m := range t.Messages {
			thread.Messages = append(thread.Messages, &v1.ReviewMessage{Id: m.ID, Role: m.Role, Body: m.Body, RevisionId: m.Revision, RoundId: m.Round, Addressed: m.Addressed, Resolution: m.Resolution, CreatedAt: stamp(m.CreatedAt)})
		}
		out.Threads = append(out.Threads, thread)
	}
	for _, q := range state.Rounds {
		out.Rounds = append(out.Rounds, reviewRound(q))
	}
	return response(out, nil)
}
func (s *Server) GetReviewDrafts(ctx context.Context, req *connect.Request[v1.GetReviewDraftsRequest]) (*connect.Response[v1.GetReviewDraftsResponse], error) {
	state, err := s.App.ReviewRecordsFor(ctx, resolve(req.Msg.ItemId))
	if err != nil {
		return nil, checkpointError(err)
	}
	out := &v1.GetReviewDraftsResponse{Version: uint32(state.Version)}
	for _, d := range state.Drafts {
		out.Drafts = append(out.Drafts, reviewDraft(d))
	}
	return response(out, nil)
}
func (s *Server) SaveReviewDraft(ctx context.Context, req *connect.Request[v1.SaveReviewDraftRequest]) (*connect.Response[v1.SaveReviewDraftResponse], error) {
	r := req.Msg
	if r.Draft == nil {
		return nil, invalid("review draft required")
	}
	d := r.Draft
	out, err := s.App.MutateReview(ctx, app.ReviewMutationOptions{ResolveOptions: resolve(r.ItemId), ExpectedVersion: uint64(r.ExpectedVersion), Mutation: reviewcore.Mutation{OperationID: r.OperationId, Kind: "save_draft", Draft: reviewcore.Draft{ID: d.Id, Thread: d.ThreadId, Anchor: coreAnchor(d.Anchor), Body: d.Body, Resolution: d.Resolution}}})
	if err != nil {
		return nil, checkpointError(err)
	}
	return response(&v1.SaveReviewDraftResponse{Result: reviewMutation(out)}, nil)
}
func (s *Server) DeleteReviewDrafts(ctx context.Context, req *connect.Request[v1.DeleteReviewDraftsRequest]) (*connect.Response[v1.DeleteReviewDraftsResponse], error) {
	r := req.Msg
	out, err := s.App.MutateReview(ctx, app.ReviewMutationOptions{ResolveOptions: resolve(r.ItemId), ExpectedVersion: uint64(r.ExpectedVersion), Mutation: reviewcore.Mutation{OperationID: r.OperationId, Kind: "delete_draft", DraftIDs: r.DraftIds}})
	if err != nil {
		return nil, checkpointError(err)
	}
	return response(&v1.DeleteReviewDraftsResponse{Result: reviewMutation(out)}, nil)
}
func (s *Server) PublishReviewRound(ctx context.Context, req *connect.Request[v1.PublishReviewRoundRequest]) (*connect.Response[v1.PublishReviewRoundResponse], error) {
	r := req.Msg
	out, err := s.App.MutateReview(ctx, app.ReviewMutationOptions{ResolveOptions: resolve(r.ItemId), ExpectedVersion: uint64(r.ExpectedVersion), Mutation: reviewcore.Mutation{OperationID: r.OperationId, Kind: "publish", Revision: r.RevisionId, FromRevision: r.FromRevisionId, DraftIDs: r.DraftIds}})
	if err != nil {
		return nil, checkpointError(err)
	}
	if !out.Reused {
		if _, err := s.notifyReviewRound(ctx, r.ItemId, out.Receipt.Round, "review-"+r.OperationId); err != nil {
			return nil, partial(connect.NewError(connect.CodeOf(err), fmt.Errorf("published review round %s; notification failed: %s", out.Receipt.Round, err)), r.ItemId, "published review round "+out.Receipt.Round+"; notification failed")
		}
	}
	return response(&v1.PublishReviewRoundResponse{Result: reviewMutation(out)}, nil)
}
func (s *Server) ReplyReviewThreads(ctx context.Context, req *connect.Request[v1.ReplyReviewThreadsRequest]) (*connect.Response[v1.ReplyReviewThreadsResponse], error) {
	r := req.Msg
	opts := app.ReviewMutationOptions{ResolveOptions: resolve(r.ItemId), ExpectedVersion: uint64(r.ExpectedVersion), Capture: r.Capture, ClaimID: r.ExpectedClaimId, ClaimGeneration: uint64(r.ExpectedClaimGeneration), Mutation: reviewcore.Mutation{OperationID: r.OperationId, Kind: "reply", Revision: r.RevisionId}}
	for _, reply := range r.Replies {
		if reply == nil {
			return nil, invalid("reply required")
		}
		opts.Mutation.Replies = append(opts.Mutation.Replies, reviewcore.Reply{Thread: reply.ThreadId, Body: reply.Body, Addressed: reply.Addressed})
	}
	out, err := s.App.MutateReview(ctx, opts)
	if err != nil {
		return nil, checkpointError(err)
	}
	return response(&v1.ReplyReviewThreadsResponse{Result: reviewMutation(out)}, nil)
}
