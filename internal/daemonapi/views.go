package daemonapi

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func (s *Server) WatchChanges(ctx context.Context, req *connect.Request[v1.WatchChangesRequest], stream *connect.ServerStream[v1.WatchChangesResponse]) error {
	epoch := ""
	after := uint64(0)
	if data, err := base64.RawURLEncoding.DecodeString(req.Msg.After); err == nil {
		parts := strings.SplitN(string(data), ":", 2)
		if len(parts) == 2 {
			epoch = parts[0]
			after, _ = strconv.ParseUint(parts[1], 10, 64)
		}
	}
	first := true
	for {
		value, changed := s.Core.Database().ChangesSnapshot(epoch, after)
		if first || value.All || len(value.ItemIDs) > 0 {
			if first && req.Msg.After == "" {
				value.All = false
				value.ItemIDs = nil
			}
			out := &v1.WatchChangesResponse{Cursor: base64.RawURLEncoding.EncodeToString([]byte(value.Epoch + ":" + strconv.FormatUint(value.Revision, 10))), ItemIds: value.ItemIDs, All: value.All, ProfilesChanged: value.All}
			if err := stream.Send(out); err != nil {
				return err
			}
			epoch, after = value.Epoch, value.Revision
		}
		first = false
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
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
