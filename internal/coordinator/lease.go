package coordinator

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/regb/workitem/internal/model"
)

const (
	MethodLeaseGet     = "lease_get"
	MethodLeaseAcquire = "lease_acquire"
	MethodLeaseRelease = "lease_release"

	// MaxLeaseTTL bounds renewable leases so an abandoned browser tab cannot
	// hold control for long.
	MaxLeaseTTL = 10 * time.Minute
)

// leaseTable keeps controller leases in memory. They are observations about
// connected clients; losing them on restart is safe.
type leaseTable struct {
	mu     sync.Mutex
	leases map[string]model.ControlLease
}

func newLeaseTable() *leaseTable {
	return &leaseTable{leases: map[string]model.ControlLease{}}
}

func (t *leaseTable) get(itemID string, now time.Time) *model.ControlLease {
	t.mu.Lock()
	defer t.mu.Unlock()
	lease, ok := t.leases[itemID]
	if !ok {
		return nil
	}
	if !lease.Live(now) {
		delete(t.leases, itemID)
		return nil
	}
	return &lease
}

func validateLeaseRequest(request model.LeaseRequest) error {
	if !model.ValidID(request.ItemID) {
		return errors.New("valid item_id is required")
	}
	if request.Holder != model.LeaseHolderWeb && request.Holder != model.LeaseHolderTerminal {
		return errors.New("lease holder must be web or terminal")
	}
	if strings.TrimSpace(request.ClientID) == "" || compactIdentifier(request.ClientID, 128) != request.ClientID {
		return errors.New("valid client_id is required")
	}
	if len(request.Label) > 200 {
		return errors.New("lease label is too long")
	}
	if request.TTLSeconds < 0 || request.TTLSeconds > int(MaxLeaseTTL/time.Second) {
		return errors.New("lease ttl_seconds is out of range")
	}
	return nil
}

// acquire grants the lease when it is vacant, expired, already held by the
// same client (a renewal), or when the request takes over explicitly.
func (t *leaseTable) acquire(request model.LeaseRequest, now time.Time) (model.LeaseResult, error) {
	if err := validateLeaseRequest(request); err != nil {
		return model.LeaseResult{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	current, held := t.leases[request.ItemID]
	if held && !current.Live(now) {
		held = false
	}
	actualID := ""
	if held {
		actualID = current.ID
	}
	if request.ExpectedLeaseID != nil && *request.ExpectedLeaseID != actualID {
		result := model.LeaseResult{}
		if held {
			copy := current
			result.Lease = &copy
		}
		return result, nil
	}
	if held && !current.SameClient(request.Holder, request.ClientID) && !request.Takeover {
		copy := current
		return model.LeaseResult{Granted: false, Lease: &copy}, nil
	}
	leaseID, err := model.NewID()
	if err != nil {
		return model.LeaseResult{}, err
	}
	lease := model.ControlLease{ID: leaseID, ItemID: request.ItemID, Holder: request.Holder, ClientID: request.ClientID, Label: strings.TrimSpace(request.Label), AcquiredAt: now, RenewedAt: now}
	result := model.LeaseResult{Granted: true}
	if held {
		if current.SameClient(request.Holder, request.ClientID) {
			lease.AcquiredAt = current.AcquiredAt
			lease.ID = current.ID
		} else {
			previous := current
			result.Previous = &previous
		}
	}
	if request.TTLSeconds > 0 {
		expires := now.Add(time.Duration(request.TTLSeconds) * time.Second)
		lease.ExpiresAt = &expires
	}
	t.leases[request.ItemID] = lease
	result.Lease = &lease
	return result, nil
}

// release drops the lease only when the caller still holds it.
func (t *leaseTable) release(request model.LeaseRequest) (model.LeaseResult, error) {
	if !model.ValidID(request.ItemID) {
		return model.LeaseResult{}, errors.New("valid item_id is required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	current, held := t.leases[request.ItemID]
	if !held || request.ExpectedLeaseID != nil && *request.ExpectedLeaseID != current.ID || !current.SameClient(request.Holder, request.ClientID) && !request.Takeover {
		return model.LeaseResult{Granted: false}, nil
	}
	delete(t.leases, request.ItemID)
	previous := current
	return model.LeaseResult{Granted: true, Previous: &previous}, nil
}
