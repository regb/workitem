package daemonapi

import (
	"context"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func (s *Server) startItem(ctx context.Context, itemID string, options *v1.StartOptions, expected uint32) (*v1.StartItemResponse, error) {
	if err := validateStart(options); err != nil {
		return nil, err
	}
	if options == nil {
		options = &v1.StartOptions{}
	}
	current, err := s.canonical(ctx, itemID, expected)
	if err != nil {
		return nil, err
	}
	if current.Manifest.State == model.StateClosed {
		return nil, precondition("closed item must be reopened first")
	}
	if current.Manifest.State == model.StateBacklog {
		if _, err := s.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: itemID, State: v1.ItemState_ITEM_STATE_ACTIVE, ExpectedRevision: expected, OverrideCapacity: options.OverrideCapacity})); err != nil {
			return nil, err
		}
	}
	who := caller(ctx)
	key := options.ClientMessageId
	if key == "" {
		if options.SendNothing {
			key = "resume:" + mode(options.Mode)
		} else {
			key = id()
		}
	}
	job := s.startJobKey("start_item", "start_item:"+key, itemID, "", func(ctx context.Context, j *jobRun) error {
		ctx = context.WithValue(ctx, identityKey{}, who)
		unlock := s.lock(itemID)
		defer unlock()
		j.phase("claiming workspace")
		completed := []string{"activated item"}
		if _, err := s.requireActive(ctx, itemID); err != nil {
			return partial(err, itemID, completed...)
		}
		if _, err := s.App.EnsureWorkItemWorkspace(ctx, resolve(itemID)); err != nil {
			return partial(err, itemID, completed...)
		}
		completed = append(completed, "claimed workspace")
		j.phase("setting up checkout")
		if options.SkipSetup {
			ctx = app.WithSkipSetup(ctx)
		}
		if _, err := s.App.SetupWorkspace(ctx, resolve(itemID), false); err != nil {
			return partial(err, itemID, completed...)
		}
		completed = append(completed, "set up workspace")
		j.phase("starting agent")
		if err := s.startAgent(ctx, itemID, options.Mode); err != nil {
			return partial(err, itemID, completed...)
		}
		if options.SendNothing {
			return nil
		}
		// A new agent is busy, or not yet observed, while it starts; its first
		// prompt waits until it is idle rather than being refused.
		j.phase("waiting for the agent")
		s.waitAgentIdle(ctx, itemID, agentStartIdleTimeout)
		j.phase("delivering startup message")
		text := s.App.AgentStartupMessage(options.Prompt)
		return partial(s.send(ctx, &v1.SendMessageRequest{ItemId: itemID, Text: text, Delivery: v1.Delivery_DELIVERY_PROMPT, ClientMessageId: options.ClientMessageId, StartMode: options.Mode}), itemID, append(completed, "started agent")...)
	})
	value, err := s.item(ctx, itemID)
	if err != nil {
		return nil, partial(err, itemID, "activated item")
	}
	return &v1.StartItemResponse{Item: value, Job: job}, nil
}
func (s *Server) StartItem(ctx context.Context, req *connect.Request[v1.StartItemRequest]) (*connect.Response[v1.StartItemResponse], error) {
	out, err := s.startItem(ctx, req.Msg.ItemId, req.Msg.Start, req.Msg.ExpectedRevision)
	return response(out, err)
}
func (s *Server) CreateAndStartItem(ctx context.Context, req *connect.Request[v1.CreateAndStartItemRequest]) (*connect.Response[v1.CreateAndStartItemResponse], error) {
	if req.Msg.Item == nil {
		return nil, invalid("item is required")
	}
	if err := validateStart(req.Msg.Start); err != nil {
		return nil, err
	}
	created, err := s.CreateItem(ctx, connect.NewRequest(req.Msg.Item))
	if err != nil {
		return nil, err
	}
	unlock := s.lock(created.Msg.Item.Id)
	defer unlock()
	started, err := s.startItem(ctx, created.Msg.Item.Id, req.Msg.Start, 0)
	if err != nil {
		return nil, partial(err, created.Msg.Item.Id, "created item")
	}
	return response(&v1.CreateAndStartItemResponse{Item: started.Item, Job: started.Job}, nil)
}
func (s *Server) shelveItem(ctx context.Context, req *connect.Request[v1.ShelveItemRequest]) (*connect.Response[v1.ShelveItemResponse], error) {
	if err := s.confirmTerminal(ctx, req.Msg.ItemId, req.Msg.ConfirmAttachedTerminal); err != nil {
		return nil, rpcError(err)
	}
	result, err := s.App.Shelve(ctx, resolve(req.Msg.ItemId), false)
	if err != nil {
		return nil, partial(err, req.Msg.ItemId, result.Warnings...)
	}
	value, err := s.item(ctx, req.Msg.ItemId)
	return response(&v1.ShelveItemResponse{Item: value, Warnings: result.Warnings}, err)
}
func (s *Server) closeItem(ctx context.Context, req *connect.Request[v1.CloseItemRequest]) (*connect.Response[v1.CloseItemResponse], error) {
	r := req.Msg
	if r.Resolution < 1 || r.Resolution > 3 {
		return nil, invalid("closing requires a resolution")
	}
	if err := s.confirmTerminal(ctx, r.ItemId, r.ConfirmAttachedTerminal); err != nil {
		return nil, rpcError(err)
	}
	commit := ""
	completed := []string{}
	if r.CommitChanges {
		if !r.Force && s.App.AgentBusy(ctx, r.ItemId) {
			return nil, precondition("agent is busy")
		}
		if _, err := s.App.StopAgentRuntime(ctx, resolve(r.ItemId), r.Force); err != nil {
			return nil, rpcError(err)
		}
		if err := s.App.WaitForAgentStopped(ctx, r.ItemId); err != nil {
			return nil, partial(err, r.ItemId, "requested agent shutdown")
		}
		completed = append(completed, "stopped agent")
		current, err := s.App.Store.LoadManifest(r.ItemId)
		if err != nil {
			return nil, rpcError(err)
		}
		if current.Checkout.Claim != nil {
			var err error
			commit, _, err = s.App.CommitWorkItemWorkspace(ctx, resolve(r.ItemId), "")
			if err != nil {
				return nil, partial(err, r.ItemId, completed...)
			}
			if commit != "" {
				completed = append(completed, "committed workspace changes")
			}
		}
	}
	result, err := s.App.Close(ctx, resolve(r.ItemId), app.CloseOptions{Resolution: resolutionName(r.Resolution), Force: r.Force})
	if err != nil {
		return nil, partial(err, r.ItemId, append(completed, result.Warnings...)...)
	}
	value, err := s.item(ctx, r.ItemId)
	return response(&v1.CloseItemResponse{Item: value, Commit: commit, Warnings: result.Warnings}, err)
}
func (s *Server) ReopenItem(ctx context.Context, req *connect.Request[v1.ReopenItemRequest]) (*connect.Response[v1.ReopenItemResponse], error) {
	c, err := s.canonical(ctx, req.Msg.ItemId, 0)
	if err != nil {
		return nil, rpcError(err)
	}
	if c.Manifest.State == model.StateActive {
		return nil, precondition("item is already active")
	}
	if _, err := s.SetItemState(ctx, connect.NewRequest(&v1.SetItemStateRequest{ItemId: req.Msg.ItemId, State: v1.ItemState_ITEM_STATE_BACKLOG})); err != nil {
		return nil, err
	}
	if req.Msg.Start {
		started, err := s.startItem(ctx, req.Msg.ItemId, &v1.StartOptions{SendNothing: true, Mode: req.Msg.Mode}, 0)
		if err != nil {
			return nil, partial(err, req.Msg.ItemId, "reopened item")
		}
		return response(&v1.ReopenItemResponse{Item: started.Item, Job: started.Job}, nil)
	}
	value, err := s.item(ctx, req.Msg.ItemId)
	return response(&v1.ReopenItemResponse{Item: value}, err)
}

// agentStartIdleTimeout bounds how long a start waits for its new agent to be
// idle before delivering the first prompt anyway.
const agentStartIdleTimeout = 2 * time.Minute

// waitAgentIdle waits until the item's agent is observed idle, the timeout
// passes, or ctx ends. Delivery reports any refusal that remains.
func (s *Server) waitAgentIdle(ctx context.Context, itemID string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for s.App.AgentBusy(ctx, itemID) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}
