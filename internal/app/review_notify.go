package app

import (
	"context"
	"errors"
	"fmt"
	reviewcore "github.com/regb/workitem/internal/app/core/review"
	"github.com/regb/workitem/internal/model"
	"time"
)

var ErrReviewNotificationUnavailable = errors.New("review notification unavailable")

type ReviewNotificationOptions struct {
	ResolveOptions
	RoundID, MessageID, ClientID, Holder string
}
type ReviewNotification struct {
	ItemID, Text, MessageID, LeaseID string
	Generation                       uint32
	FollowUp                         bool
}

// NotifyReviewRound composes existing control and delivery policy. It never
// starts an agent, displaces a controller, or schedules a retry.
func (a *App) NotifyReviewRound(ctx context.Context, opts ReviewNotificationOptions, send func(context.Context, ReviewNotification) (string, error)) (reviewcore.Round, error) {
	fail := func(reason string) (reviewcore.Round, error) {
		return reviewcore.Round{}, fmt.Errorf("%w: %s", ErrReviewNotificationUnavailable, reason)
	}
	state, err := a.ReviewRecordsFor(ctx, opts.ResolveOptions)
	if err != nil {
		return reviewcore.Round{}, err
	}
	q, err := state.Round(opts.RoundID)
	if err != nil {
		return reviewcore.Round{}, err
	}
	runtime, err := a.AgentRuntimeStatus(ctx, opts.ResolveOptions)
	if err != nil {
		return reviewcore.Round{}, err
	}
	if !runtime.Online || runtime.Runtime == nil {
		return fail("no agent is running; use wi review notify to send later")
	}
	status, err := a.ControlStatus(ctx, opts.ResolveOptions)
	if err != nil {
		return reviewcore.Round{}, err
	}
	if status.Implicit || status.TerminalClients > 0 && status.Lease == nil {
		return fail("agent is controlled by a terminal")
	}
	lease := status.Lease
	if lease != nil {
		if lease.ClientID != opts.ClientID || lease.Holder != opts.Holder {
			return fail("another client controls the agent")
		}
	} else {
		request := ControlRequest{ResolveOptions: opts.ResolveOptions, Holder: opts.Holder, ClientID: opts.ClientID, EnsureRuntime: false, TTL: WebLeaseTTL}
		_, plan, err := a.PlanControl(ctx, request)
		if err != nil {
			return reviewcore.Round{}, err
		}
		if plan.NeedsConfirmation || plan.Replace != "" || plan.Start != "" || plan.Abort || plan.Unavailable != "" {
			return fail("notification requires a running controllable agent")
		}
		granted, err := a.AcquireControl(ctx, request)
		if err != nil {
			return reviewcore.Round{}, err
		}
		lease = granted.Lease
		if lease == nil {
			return fail("no controller lease")
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			_, _ = a.Leases.ReleaseLease(releaseCtx, model.LeaseRequest{ItemID: state.ItemID, Holder: opts.Holder, ClientID: opts.ClientID, ExpectedLeaseID: &lease.ID})
		}()
	}
	text := fmt.Sprintf("Review feedback is published for item %s, round %s, revision %s. Read it with `wi review threads --item %s --unresolved --json` and `wi review diff %s --item %s --patch`. Reply with `wi review reply`. Addressed evidence does not resolve reviewer concerns.", state.ItemID, q.ID, q.Revision, state.ItemID, q.Revision, state.ItemID)
	command, err := send(ctx, ReviewNotification{ItemID: state.ItemID, Text: text, MessageID: opts.MessageID, LeaseID: lease.ID, Generation: runtime.Runtime.Generation, FollowUp: status.Busy})
	if err != nil {
		return reviewcore.Round{}, err
	}
	if err := a.MarkReviewNotified(ctx, opts.ResolveOptions, q.ID, command); err != nil {
		return reviewcore.Round{}, fmt.Errorf("notification sent as command %s; recording its receipt failed: %w", command, err)
	}
	state, err = a.ReviewRecordsFor(ctx, opts.ResolveOptions)
	if err != nil {
		return reviewcore.Round{}, err
	}
	q, err = state.Round(opts.RoundID)
	if err != nil {
		return reviewcore.Round{}, err
	}
	return *q, nil
}
