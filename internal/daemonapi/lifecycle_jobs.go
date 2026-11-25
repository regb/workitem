package daemonapi

import (
	"connectrpc.com/connect"
	"context"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
)

func (s *Server) ShelveItem(ctx context.Context, req *connect.Request[v1.ShelveItemRequest]) (*connect.Response[v1.ShelveItemResponse], error) {
	c, err := s.canonical(ctx, req.Msg.ItemId, req.Msg.ExpectedRevision)
	if err != nil {
		return nil, rpcError(err)
	}
	value := item(c.Manifest, c.Revision)
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
	job := s.startJob(ctx, "shelve_item", value.Id, value.Repository.Id, func(ctx context.Context, j *jobRun) error {
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
	c, err := s.canonical(ctx, req.Msg.ItemId, req.Msg.ExpectedRevision)
	if err != nil {
		return nil, rpcError(err)
	}
	value := item(c.Manifest, c.Revision)
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
	job := s.startJobKey(ctx, "close_item", "close_item:"+req.Msg.Resolution.String(), value.Id, value.Repository.Id, func(ctx context.Context, j *jobRun) error {
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

func (s *Server) PlanClose(ctx context.Context, req *connect.Request[v1.PlanCloseRequest]) (*connect.Response[v1.PlanCloseResponse], error) {
	m, err := s.App.Store.LoadManifest(req.Msg.ItemId)
	if err != nil {
		return nil, rpcError(err)
	}
	out := &v1.PlanCloseResponse{}
	if m.State == model.StateClosed {
		return response(out, nil)
	}
	runtime, err := s.App.AgentRuntimeStatus(ctx, resolve(m.ID))
	if err != nil {
		return nil, rpcError(err)
	}
	out.AbortsTurn = runtime.Online && s.App.AgentBusy(ctx, m.ID)
	changes, err := s.App.ItemUncommittedChanges(ctx, resolve(m.ID))
	if err != nil {
		return nil, rpcError(err)
	}
	out.Commits, out.RemovesAttachedCopies = changes.Files, changes.Attached
	status, err := s.App.ControlStatus(ctx, resolve(m.ID))
	if err != nil {
		return nil, rpcError(err)
	}
	out.AttachedTerminalClients = uint32(status.TerminalClients)
	if o := s.observation(ctx, m); o.BranchMismatch {
		out.CheckoutProblem = "the checkout is on another branch"
	} else if o.WorktreeStatus == v1.WorktreeStatus_WORKTREE_STATUS_PROBLEM {
		out.CheckoutProblem = o.WorktreeReason
		if out.CheckoutProblem == "" {
			out.CheckoutProblem = "the checkout has a problem"
		}
	}
	return response(out, nil)
}
