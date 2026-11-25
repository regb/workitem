package daemonapi

import (
	"context"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func (s *Server) GetItemView(ctx context.Context, req *connect.Request[v1.GetItemViewRequest]) (*connect.Response[v1.GetItemViewResponse], error) {
	// A stable item revision brackets the composite read. External observations
	// remain observations and may advance independently of durable item intent.
	for attempt := 0; attempt < 4; attempt++ {
		readAt := time.Now().UTC()
		current, err := s.GetItem(ctx, connect.NewRequest(&v1.GetItemRequest{ItemId: req.Msg.ItemId, IncludeDescription: true}))
		if err != nil {
			return nil, err
		}
		a, control, warnings, err := s.agent(ctx, req.Msg.ItemId)
		if err != nil {
			return nil, rpcError(err)
		}
		out := &v1.GetItemViewResponse{Item: current.Msg.Item, Description: current.Msg.Description, Observation: current.Msg.Observation, Agent: a, Control: control, Setup: s.setup(ctx, req.Msg.ItemId), Jobs: s.jobs.list(req.Msg.ItemId, true), ReadAt: stamp(readAt), Warnings: warnings}
		if proposals, err := s.Core.Database().ListProposals(req.Msg.ItemId, false); err == nil {
			for _, value := range proposals {
				out.Proposals = append(out.Proposals, s.proposal(value))
			}
		}
		if out.Item.Conversation != nil {
			h, err := s.GetHistory(ctx, connect.NewRequest(&v1.GetHistoryRequest{ItemId: req.Msg.ItemId, LeafId: req.Msg.HistoryLeafId}))
			if err != nil {
				out.Warnings = append(out.Warnings, err.Error())
			} else {
				out.History = h.Msg
			}
		} else {
			out.History = &v1.GetHistoryResponse{Messages: []*v1.ConversationMessage{}}
		}
		after, err := s.canonical(ctx, req.Msg.ItemId, 0)
		if err != nil {
			return nil, rpcError(err)
		}
		if uint32(after.Revision) == out.Item.Revision {
			return response(out, nil)
		}
	}
	return nil, aborted("item changed repeatedly while reading its view")
}
