package daemonapi

import (
	"context"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/credential"
	"github.com/regb/workitem/internal/model"
)

// actOnItself is the identity the daemon acts under when it sends on its
// own, for a schedule's run or a follow-up. It acts as the user the work is
// for, with what caused it recorded (origin), and for the client
// that holds the item's control, if one does, so it never displaces it;
// otherwise as its own client, a command-line holder, since a request has
// none. The returned done gives back control taken as its own client, once
// the job the send started, if any, has finished: no client then finds the
// item controlled by the daemon.
func (s *Server) actOnItself(ctx context.Context, itemID, client, label string, origin model.Origin) (context.Context, func(job *v1.Job)) {
	who := identity{principal: principal{kind: credential.Operator}, client: client, holder: model.LeaseHolderCLI, label: label}
	if itemID != "" {
		if lease, err := s.Local.Lease(ctx, itemID); err == nil && lease != nil {
			who.client, who.holder = lease.ClientID, lease.Holder
		}
	}
	done := func(job *v1.Job) {
		target := itemID
		if job != nil && job.ItemId != "" {
			target = job.ItemId
		}
		if target == "" || who.client != client {
			return
		}
		release := func() {
			if lease, err := s.Local.Lease(s.ctx, target); err == nil && lease != nil && lease.ClientID == client {
				expected := lease.ID
				_, _ = s.Local.ReleaseLease(s.ctx, model.LeaseRequest{ItemID: target, Holder: lease.Holder, ClientID: lease.ClientID, ExpectedLeaseID: &expected})
			}
		}
		if job == nil || terminalJob(job.State) {
			release()
			return
		}
		if !s.startCallback() {
			return
		}
		go func() {
			defer s.callbacks.Done()
			s.jobOutcome(s.ctx, job.Id)
			release()
		}()
	}
	return model.WithOrigin(context.WithValue(ctx, identityKey{}, who), origin), done
}

// jobOutcome waits for a job to finish and returns it, or nil when it is
// unknown or ctx ends first.
func (s *Server) jobOutcome(ctx context.Context, id string) *v1.Job {
	run, err := s.jobs.get(id)
	if err != nil {
		return nil
	}
	for {
		value, changed := run.snapshot()
		if terminalJob(value.State) {
			return value
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		}
	}
}
