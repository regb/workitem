package app

import (
	"context"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/model"
)

// admitControllerCommand checks structured submissions as well as terminal
// entry. Plain CLI commands share the terminal identity, matching tmux's shared
// controller model. A one-shot CLI send takes a vacant lease only for delivery.
func (a *App) admitControllerCommand(ctx context.Context, itemID string, opts AgentControlOptions) (func(), error) {
	noop := func() {}
	holder, clientID := opts.ControllerHolder, opts.ControllerClientID
	explicit := holder != "" || clientID != "" || opts.ControllerLeaseID != ""
	if a.Leases == nil {
		if explicit {
			return noop, fmt.Errorf("controller leases are unavailable")
		}
		return noop, nil
	}
	if !explicit {
		holder, clientID = model.LeaseHolderTerminal, "terminal"
	} else if holder == "" || clientID == "" || opts.ControllerLeaseID == "" {
		return noop, fmt.Errorf("controller holder, client ID, and lease ID are required")
	}
	current, err := a.Leases.Lease(ctx, itemID)
	if err != nil {
		return noop, fmt.Errorf("read controller before submission: %w", err)
	}
	expected := ""
	if current != nil {
		if !current.SameClient(holder, clientID) || explicit && current.ID != opts.ControllerLeaseID {
			return noop, fmt.Errorf("another controller holds this agent; take control explicitly before sending")
		}
		expected = current.ID
	} else if explicit {
		return noop, fmt.Errorf("controller lease expired or was released; reacquire control before sending")
	}
	ttl := 0
	if holder == model.LeaseHolderWeb {
		ttl = int(WebLeaseTTL / time.Second)
	}
	granted, err := a.Leases.AcquireLease(ctx, model.LeaseRequest{ItemID: itemID, Holder: holder, ClientID: clientID, TTLSeconds: ttl, ExpectedLeaseID: &expected})
	if err != nil {
		return noop, fmt.Errorf("admit controller command: %w", err)
	}
	if !granted.Granted || granted.Lease == nil {
		return noop, fmt.Errorf("controller changed before submission; retry after acquiring control")
	}
	if current != nil {
		return noop, nil
	}
	leaseID := granted.Lease.ID
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, _ = a.Leases.ReleaseLease(releaseCtx, model.LeaseRequest{ItemID: itemID, Holder: holder, ClientID: clientID, ExpectedLeaseID: &leaseID})
	}, nil
}
