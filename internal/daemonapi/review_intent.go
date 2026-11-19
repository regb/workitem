package daemonapi

import (
	"connectrpc.com/connect"
	"context"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/app"
	reviewcore "github.com/regb/workitem/internal/app/core/review"
)

func (s *Server) GetReviewIntent(ctx context.Context, req *connect.Request[v1.GetReviewIntentRequest]) (*connect.Response[v1.GetReviewIntentResponse], error) {
	r := req.Msg
	state, err := s.App.ReviewRecordsFor(ctx, resolve(r.ItemId))
	if err != nil {
		return nil, checkpointError(err)
	}
	if _, err := state.Find(r.RevisionId); err != nil {
		return nil, checkpointError(err)
	}
	out := &v1.GetReviewIntentResponse{Version: uint32(state.Version), Approved: state.Approved(r.RevisionId)}
	for _, a := range state.Approvals {
		if a.Revision == r.RevisionId {
			out.Approvals = append(out.Approvals, &v1.ReviewApproval{Id: a.ID, RevisionId: a.Revision, Kind: a.Kind, Message: a.Message, CreatedAt: stamp(a.CreatedAt)})
		}
	}
	return response(out, nil)
}
func (s *Server) SetReviewIntent(ctx context.Context, req *connect.Request[v1.SetReviewIntentRequest]) (*connect.Response[v1.SetReviewIntentResponse], error) {
	r := req.Msg
	out, err := s.App.MutateReview(ctx, app.ReviewMutationOptions{ResolveOptions: resolve(r.ItemId), ExpectedVersion: uint64(r.ExpectedVersion), Mutation: reviewcore.Mutation{OperationID: r.OperationId, Kind: "intent", Revision: r.RevisionId, Intent: r.Kind, Message: r.Message}})
	if err != nil {
		return nil, checkpointError(err)
	}
	return response(&v1.SetReviewIntentResponse{Result: reviewMutation(out)}, nil)
}
