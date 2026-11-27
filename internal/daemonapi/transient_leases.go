package daemonapi

import (
	"context"
	"sync"

	"github.com/regb/workitem/internal/model"
)

// A command line takes an item's vacant control only to deliver what it
// sends: an agent's wi send, a notice, a schedule's run. It gives it back
// once the request, or the job it started, is done, so a person's browser
// tab or terminal finds control free rather than held for a lease's time by
// a command nobody is at. Control taken on purpose, with AcquireControl,
// is kept.

type transientLeasesKey struct{}

type transientLeases struct {
	mu     sync.Mutex
	leases []model.ControlLease
}

// withTransientLeases collects the control a command line takes during ctx;
// release gives it back.
func (s *Server) withTransientLeases(ctx context.Context) (context.Context, func()) {
	taken := &transientLeases{}
	return context.WithValue(ctx, transientLeasesKey{}, taken), func() {
		taken.mu.Lock()
		leases := taken.leases
		taken.leases = nil
		taken.mu.Unlock()
		for _, lease := range leases {
			expected := lease.ID
			// A lease already replaced or expired stays as it is.
			_, _ = s.Local.ReleaseLease(context.Background(), model.LeaseRequest{ItemID: lease.ItemID, Holder: lease.Holder, ClientID: lease.ClientID, ExpectedLeaseID: &expected})
		}
	}
}

// tookTransiently records control a command line took to deliver a request.
func tookTransiently(ctx context.Context, lease *model.ControlLease) {
	if lease == nil || lease.Holder != model.LeaseHolderCLI {
		return
	}
	if taken, ok := ctx.Value(transientLeasesKey{}).(*transientLeases); ok {
		taken.mu.Lock()
		taken.leases = append(taken.leases, *lease)
		taken.mu.Unlock()
	}
}
