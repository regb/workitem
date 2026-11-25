package daemonapi

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func (s *Server) controlRequest(ctx context.Context, itemID, client string, confirm, start bool) app.ControlRequest {
	who := caller(ctx)
	if client == "" {
		client = who.client
	}
	return app.ControlRequest{ResolveOptions: resolve(itemID), Holder: who.holder, ClientID: client, Label: labelOr(who.label, who.holder), Confirm: confirm, EnsureRuntime: start, TTL: app.WebLeaseTTL}
}
func lease(value *model.ControlLease) *v1.ControlLease {
	if value == nil {
		return nil
	}
	return &v1.ControlLease{Id: value.ID, ItemId: value.ItemID, Holder: value.Holder, ClientId: value.ClientID, Label: value.Label, AcquiredAt: stamp(value.AcquiredAt), ExpiresAt: optionalStamp(value.ExpiresAt)}
}
func (s *Server) controlStatus(ctx context.Context, status app.ControlStatus, plan app.ControlPlan, client string) *v1.ControlStatus {
	return &v1.ControlStatus{Lease: lease(status.Lease), Implicit: status.Implicit, Yours: status.Lease != nil && status.Lease.ClientID == client, Plan: &v1.ControlPlan{PlanToken: s.planToken(ctx, status, plan, client), NeedsConfirmation: plan.NeedsConfirmation, Reasons: plan.Reasons, Displaces: lease(plan.Displaces), DisplacesTerminal: plan.DisplacesTerminal, AbortsTurn: plan.Abort, RestartsIn: modeValue(plan.Replace), StartsIn: modeValue(plan.Start), Unavailable: plan.Unavailable}}
}

// refuseControl is a control refusal carrying the item's current control
// status as a detail, so a client can show who holds control and offer to
// take it without another call.
func (s *Server) refuseControl(ctx context.Context, itemID string, code connect.Code, message string) error {
	refusal := connect.NewError(code, errors.New(message))
	request := s.controlRequest(ctx, itemID, caller(ctx).client, false, false)
	status, plan, err := s.App.PlanControl(ctx, request)
	if err != nil {
		return refusal
	}
	s.rememberLease(status.Lease)
	if detail, err := connect.NewErrorDetail(s.controlStatus(ctx, status, plan, request.ClientID)); err == nil {
		refusal.AddDetail(detail)
	}
	return refusal
}
func (s *Server) rememberLease(value *model.ControlLease) {
	if value != nil {
		copy := *value
		s.leases.Store(value.ID, &copy)
	}
}
func (s *Server) GetControl(ctx context.Context, req *connect.Request[v1.GetControlRequest]) (*connect.Response[v1.GetControlResponse], error) {
	request := s.controlRequest(ctx, req.Msg.ItemId, req.Msg.ClientId, false, false)
	status, plan, err := s.App.PlanControl(ctx, request)
	s.rememberLease(status.Lease)
	return response(&v1.GetControlResponse{Control: s.controlStatus(ctx, status, plan, request.ClientID)}, err)
}
func (s *Server) AcquireControl(ctx context.Context, req *connect.Request[v1.AcquireControlRequest]) (*connect.Response[v1.AcquireControlResponse], error) {
	request := s.controlRequest(ctx, req.Msg.ItemId, req.Msg.ClientId, req.Msg.Confirm, req.Msg.StartAgent)
	if request.ClientID != caller(ctx).client {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("client_id does not match the caller metadata"))
	}
	// A web caller's label is the user wi web vouches for (controlRequest
	// takes it), never what the page says.
	if !caller(ctx).principal.web() && req.Msg.Label != "" {
		request.Label = req.Msg.Label
	}
	status, plan, err := s.App.PlanControl(ctx, request)
	if err != nil {
		return nil, rpcError(err)
	}
	if request.Confirm {
		request.ValidatePlan = func(validationCtx context.Context, status app.ControlStatus, plan app.ControlPlan) error {
			return s.validatePlan(validationCtx, req.Msg.PlanToken, status, plan, request.ClientID)
		}
		if err := request.ValidatePlan(ctx, status, plan); err != nil {
			return nil, err
		}
	}
	if plan.NeedsConfirmation && !request.Confirm {
		return nil, s.refuseControl(ctx, req.Msg.ItemId, connect.CodeFailedPrecondition, "taking control requires confirmation")
	}
	if plan.Unavailable != "" {
		return nil, s.refuseControl(ctx, req.Msg.ItemId, connect.CodeFailedPrecondition, plan.Unavailable)
	}
	if request.EnsureRuntime && (!status.RuntimeOnline || plan.Replace != "") {
		m, err := s.requireActive(ctx, req.Msg.ItemId)
		if err != nil {
			return nil, rpcError(err)
		}
		if m.Checkout.Claim == nil {
			return nil, precondition("starting an agent requires a workspace claim")
		}
		setup := s.setup(ctx, m.ID)
		if setupBlocksAgent(setup) {
			return nil, precondition("workspace setup is running or failed; wait for it or retry it")
		}
		who := caller(ctx)
		job := s.startJobKey("acquire_control", "acquire_control:"+request.ClientID, m.ID, "", func(ctx context.Context, j *jobRun) error {
			ctx = context.WithValue(ctx, identityKey{}, who)
			unlock := s.lock(m.ID)
			defer unlock()
			if _, err := s.requireActive(ctx, m.ID); err != nil {
				return err
			}
			result, err := s.App.AcquireControl(ctx, request)
			s.rememberLease(result.Lease)
			if err != nil && result.Granted {
				return partial(err, m.ID, "acquired control lease")
			}
			return err
		})
		return response(&v1.AcquireControlResponse{Control: s.controlStatus(ctx, status, plan, request.ClientID), Job: job}, nil)
	}
	result, err := s.App.AcquireControl(ctx, request)
	if err != nil {
		return nil, rpcError(err)
	}
	s.rememberLease(result.Lease)
	status, plan, err = s.App.PlanControl(ctx, request)
	return response(&v1.AcquireControlResponse{Control: s.controlStatus(ctx, status, plan, request.ClientID)}, err)
}
func (s *Server) knownLease(leaseID string) (*model.ControlLease, error) {
	if leaseID == "" {
		return nil, invalid("lease_id is required")
	}
	raw, found := s.leases.Load(leaseID)
	if !found {
		return nil, aborted("lease is stale or expired")
	}
	return raw.(*model.ControlLease), nil
}
func (s *Server) RenewControl(ctx context.Context, req *connect.Request[v1.RenewControlRequest]) (*connect.Response[v1.RenewControlResponse], error) {
	value, err := s.knownLease(req.Msg.LeaseId)
	if err != nil {
		return nil, rpcError(err)
	}
	if value.ClientID != caller(ctx).client {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("lease belongs to another client"))
	}
	unlock := s.lock(value.ItemID)
	defer unlock()
	expected := value.ID
	result, err := s.Local.AcquireLease(ctx, model.LeaseRequest{ItemID: value.ItemID, Holder: value.Holder, ClientID: value.ClientID, Label: value.Label, ExpectedLeaseID: &expected, TTLSeconds: int(app.WebLeaseTTL / time.Second)})
	if err == nil && !result.Granted {
		err = aborted("control lease changed or expired")
	}
	s.rememberLease(result.Lease)
	return response(&v1.RenewControlResponse{Lease: lease(result.Lease)}, err)
}
func (s *Server) ReleaseControl(ctx context.Context, req *connect.Request[v1.ReleaseControlRequest]) (*connect.Response[v1.ReleaseControlResponse], error) {
	who := caller(ctx)
	itemID := req.Msg.ItemId
	if itemID == "" {
		value, err := s.knownLease(req.Msg.LeaseId)
		if err != nil {
			return nil, rpcError(err)
		}
		if value.ClientID != who.client {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("lease belongs to another client"))
		}
		itemID = value.ItemID
		// A request naming its item was locked on it by the interceptor.
		unlock := s.lock(itemID)
		defer unlock()
	}
	current, err := s.Local.Lease(ctx, itemID)
	if err != nil {
		return nil, rpcError(err)
	}
	if current == nil {
		return response(&v1.ReleaseControlResponse{}, nil)
	}
	if req.Msg.LeaseId != "" && current.ID != req.Msg.LeaseId {
		return nil, aborted("control lease changed")
	}
	if current.ClientID != who.client {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("lease belongs to another client"))
	}
	expected := current.ID
	result, err := s.Local.ReleaseLease(ctx, model.LeaseRequest{ItemID: itemID, Holder: current.Holder, ClientID: current.ClientID, ExpectedLeaseID: &expected})
	if err == nil && !result.Granted {
		err = aborted("control lease changed")
	}
	if err == nil {
		if _, repair := s.App.ControlStatus(ctx, resolve(itemID)); repair != nil {
			return nil, partial(repair, itemID, "released control lease")
		}
	}
	return response(&v1.ReleaseControlResponse{}, err)
}

// labelOr is label, or else fallback.
func labelOr(label, fallback string) string {
	if label != "" {
		return label
	}
	return fallback
}
