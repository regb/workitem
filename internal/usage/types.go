// Package usage exports remaining subscription quota from native clients.
// It does not track token consumption, sessions, historical usage, or costs.
package usage

import (
	"context"
	"time"
)

type State string

const (
	OK          State = "ok"
	Unsupported State = "unsupported"
	Unavailable State = "unavailable"
	Stale       State = "stale"
	ExpiredAuth State = "expired_auth"
)

type Account struct {
	Provider string `json:"provider"`
	ID       string `json:"id"` // Opaque native account/configuration binding, not a billing identifier.
}
type WindowScope struct {
	Model   string `json:"model,omitempty"`
	Surface string `json:"surface,omitempty"`
}
type Window struct {
	ID               string       `json:"id"`
	Scope            *WindowScope `json:"scope,omitempty"`
	RemainingPercent *float64     `json:"remaining_percent"` // nil means unknown, never inferred as zero.
	ResetsAt         *time.Time   `json:"resets_at"`
	DurationSeconds  *int64       `json:"duration_seconds"`
}
type Credits struct {
	Remaining *float64 `json:"remaining"`
	Unlimited *bool    `json:"unlimited"`
	Unit      string   `json:"unit"`
}

// Quota retains native diagnostics and cache scheduling internally. Public JSON
// is projected separately by NewSnapshot so cache persistence stays independent.
type Quota struct {
	IncludedUsageAllowed *bool      `json:"included_usage_allowed"` // Native permission only; never inferred from percentages/resets.
	Account              Account    `json:"account"`
	State                State      `json:"state"`
	Detail               string     `json:"detail"`
	CheckedAt            time.Time  `json:"checked_at"`
	ObservedAt           *time.Time `json:"observed_at"`
	NextRefreshAt        *time.Time `json:"next_refresh_at"`
	Windows              []Window   `json:"windows"`
	Credits              *Credits   `json:"credits"`
	// Identity or entitlement changed during a query: never retain older data.
	PreventStaleFallback bool `json:"-"`
}

// Provider is quota-only. It must not send model prompts, choose execution
// credentials, or infer remaining tokens from percentages or session activity.
type Provider interface {
	Account() Account
	Quota(context.Context) Quota
}

// AccountResolver binds a source to its current native account before filtering
// or cache lookup. Resolution must not export authentication material.
type AccountResolver interface {
	ResolveAccount(context.Context) Provider
}

// CachedQuotaValidator fences a cached observation against native account
// changes while waiting for the cache lock. It must discard data on uncertainty.
type CachedQuotaValidator interface {
	ValidateCachedQuota(context.Context, Quota) Quota
}
