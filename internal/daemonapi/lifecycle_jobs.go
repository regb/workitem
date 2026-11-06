package daemonapi

import (
	"connectrpc.com/connect"
	"context"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func (s *Server) ShelveItem(ctx context.Context, req *connect.Request[v1.ShelveItemRequest]) (*connect.Response[v1.ShelveItemResponse], error) {
	value, err := s.item(ctx, req.Msg.ItemId)
	if err != nil {
		return nil, rpcError(err)
	}
	if value.State == v1.ItemState_ITEM_STATE_BACKLOG {
		return s.shelveItem(ctx, req)
	}
	if err := s.confirmTerminal(ctx, value.Id, req.Msg.ConfirmAttachedTerminal); err != nil {
		return nil, rpcError(err)
	}
	runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(value.Id))
	if err != nil {
		return nil, rpcError(err)
	}
	if runtime.Online && s.App.AgentBusy(ctx, value.Id) {
		return nil, precondition("agent is busy")
	}
	job := s.startJob("shelve_item", value.Id, value.Repository.Id, func(ctx context.Context, j *jobRun) error {
		unlock := s.lock(value.Id)
		defer unlock()
		j.phase("shelving item")
		result, err := s.shelveItem(ctx, req)
		if err == nil {
			j.change(func(v *v1.Job) { v.Warnings = result.Msg.Warnings })
		}
		return err
	})
	return response(&v1.ShelveItemResponse{Item: value, Job: job}, nil)
}
func (s *Server) CloseItem(ctx context.Context, req *connect.Request[v1.CloseItemRequest]) (*connect.Response[v1.CloseItemResponse], error) {
	if req.Msg.Resolution < 1 || req.Msg.Resolution > 3 {
		return nil, invalid("closing requires a resolution")
	}
	value, err := s.item(ctx, req.Msg.ItemId)
	if err != nil {
		return nil, rpcError(err)
	}
	if value.State == v1.ItemState_ITEM_STATE_CLOSED {
		return s.closeItem(ctx, req)
	}
	if err := s.confirmTerminal(ctx, value.Id, req.Msg.ConfirmAttachedTerminal); err != nil {
		return nil, rpcError(err)
	}
	runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(value.Id))
	if err != nil {
		return nil, rpcError(err)
	}
	if !req.Msg.Force && runtime.Online && s.App.AgentBusy(ctx, value.Id) {
		return nil, precondition("agent is busy")
	}
	job := s.startJobKey("close_item", "close_item:"+req.Msg.Resolution.String(), value.Id, value.Repository.Id, func(ctx context.Context, j *jobRun) error {
		unlock := s.lock(value.Id)
		defer unlock()
		j.phase("closing item")
		result, err := s.closeItem(ctx, req)
		if err == nil {
			j.change(func(v *v1.Job) { v.Warnings = result.Msg.Warnings })
		}
		return err
	})
	return response(&v1.CloseItemResponse{Item: value, Job: job}, nil)
}
