package usage

import "time"

// QuotaError describes a missing or unreliable observation. Messages are already
// sanitized by the adapter; native errors and response bodies must not reach it.
type QuotaError struct {
	Code    State  `json:"code"`
	Message string `json:"message"`
}

// QuotaResult is the public representation, separate from cache scheduling.
type QuotaResult struct {
	Provider             string      `json:"provider"`
	Account              string      `json:"account,omitempty"`
	ObservedAt           *time.Time  `json:"observed_at"`
	Windows              []Window    `json:"windows"`
	IncludedUsageAllowed *bool       `json:"included_usage_allowed,omitempty"`
	Credits              *Credits    `json:"credits,omitempty"`
	Stale                bool        `json:"stale,omitempty"`
	Error                *QuotaError `json:"error,omitempty"`
}

// Snapshot is the machine-readable result of a quota query.
// Remaining values are as of ObservedAt; Stale and Error qualify their validity.
type Snapshot struct {
	SchemaVersion int           `json:"schema_version"`
	Results       []QuotaResult `json:"results"`
}

func NewSnapshot(quotas []Quota) Snapshot {
	results := make([]QuotaResult, len(quotas))
	for i, q := range quotas {
		result := QuotaResult{
			Provider: q.Account.Provider, Account: q.Account.ID,
			ObservedAt: q.ObservedAt, Windows: q.Windows,
			IncludedUsageAllowed: q.IncludedUsageAllowed, Credits: q.Credits,
			Stale: q.State == Stale,
		}
		if result.Windows == nil {
			result.Windows = []Window{}
		}
		if q.State != OK {
			code := q.State
			message := q.Detail
			switch code {
			case Unsupported, Unavailable, ExpiredAuth, Stale:
			default:
				code = Unavailable
			}
			if message == "" {
				switch code {
				case Unsupported:
					message = "Subscription quota is not supported by this source."
				case ExpiredAuth:
					message = "Native authentication was rejected."
				case Stale:
					message = "The quota observation is stale."
				default:
					message = "Subscription quota is unavailable."
				}
			}
			result.Error = &QuotaError{Code: code, Message: message}
		}
		results[i] = result
	}
	return Snapshot{SchemaVersion: 1, Results: results}
}
