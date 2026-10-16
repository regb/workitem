package model

import "time"

// Runtime control channels. A runtime exposes up to three channels: observe
// (transcript and live output, always available), control (prompt, steer,
// follow-up, abort), and terminal (a native TUI attachable through tmux).
const (
	ControlStructured = "structured"
	ControlEmulated   = "emulated"
)

type Channels struct {
	Observe bool `json:"observe"`
	// Control is "structured", "emulated", or empty when the runtime cannot
	// be controlled.
	Control string `json:"control,omitempty"`
	// AtomicIdle reports whether an idle-only prompt is enforced atomically by
	// the driver rather than checked against an observation.
	AtomicIdle bool `json:"atomic_idle"`
	Terminal   bool `json:"terminal"`
}

// ControlChannel returns the reported control channel, deriving it for
// runtimes started by drivers that predate the field: a native TUI without an
// atomic idle guard is driven by keystrokes.
func (c RuntimeCapabilities) ControlChannel() string {
	if c.Control != "" {
		return c.Control
	}
	if !c.Prompt {
		return ""
	}
	if c.NativeTUI && !c.IdleGuard {
		return ControlEmulated
	}
	return ControlStructured
}

func (c RuntimeCapabilities) Channels() Channels {
	return Channels{Observe: true, Control: c.ControlChannel(), AtomicIdle: c.IdleGuard, Terminal: c.NativeTUI}
}

// Control lease holders.
const (
	LeaseHolderWeb      = "web"
	LeaseHolderTerminal = "terminal"
)

// ControlLease names the single client allowed to send input to an item's
// runtime. Any number of other clients may observe. Leases are rebuildable
// coordinator observations, never durable item intent: a daemon restart
// leaves every runtime without a controller, and the next sender acquires it.
type ControlLease struct {
	ID         string     `json:"id"`
	ItemID     string     `json:"item_id"`
	Holder     string     `json:"holder"`
	ClientID   string     `json:"client_id"`
	Label      string     `json:"label,omitempty"`
	AcquiredAt time.Time  `json:"acquired_at"`
	RenewedAt  time.Time  `json:"renewed_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

func (l ControlLease) Live(now time.Time) bool {
	return l.ExpiresAt == nil || l.ExpiresAt.After(now)
}

func (l ControlLease) SameClient(holder, clientID string) bool {
	return l.Holder == holder && l.ClientID == clientID
}

type LeaseRequest struct {
	ItemID   string `json:"item_id"`
	Holder   string `json:"holder"`
	ClientID string `json:"client_id"`
	Label    string `json:"label,omitempty"`
	// Takeover replaces a live lease held by another client.
	Takeover bool `json:"takeover,omitempty"`
	// ExpectedLeaseID fences an observed lease, including the vacant state
	// represented by a pointer to an empty string. Nil is an unconditional request.
	ExpectedLeaseID *string `json:"expected_lease_id,omitempty"`
	// TTLSeconds bounds a lease that must be renewed; zero means it lasts
	// until released or taken over.
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

type LeaseResult struct {
	Granted  bool          `json:"granted"`
	Lease    *ControlLease `json:"lease,omitempty"`
	Previous *ControlLease `json:"previous,omitempty"`
}
