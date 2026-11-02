// Package opencode reads Go quota through a bundled OpenCode v2 server plugin.
// The native client owns OAuth, renewal, workspace selection, and HTTP requests.
package opencode

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"time"

	"github.com/regb/workitem/internal/usage"
)

//go:embed bridge/index.js
var bridge []byte

var bindingPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const bridgePath = ".opencode/plugins/wi-quota/index.js"

type nativeRunner func(context.Context, []string, []string, map[string]string, map[string][]byte) ([]byte, error)

type Adapter struct {
	Root        string
	Command     []string
	Environment map[string]string
	Now         func() time.Time
	run         nativeRunner
}

func (a *Adapter) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}
func (a *Adapter) Account() usage.Account                { return usage.LocalAccount("opencode-go", a.Root) }
func (a *Adapter) Quota(ctx context.Context) usage.Quota { return a.ResolveAccount(ctx).Quota(ctx) }

// ResolveAccount runs a native identity-only RPC before any cache lookup. A
// different selected credential/workspace receives a different cache/account ID.
func (a *Adapter) ResolveAccount(ctx context.Context) usage.Provider {
	r, err := a.rpc(ctx, "identity", struct{}{})
	p := &resolved{adapter: a, account: a.Account()}
	if err != nil {
		p.problem = &bridgeReply{State: usage.Unavailable, Reason: "native_bridge_failed"}
	} else if r.State != usage.OK || !bindingPattern.MatchString(r.Binding) {
		p.problem = &r
	} else {
		p.binding = r.Binding
		p.account = usage.LocalAccount("opencode-go", a.Root+"\x00"+r.Binding)
	}
	return p
}

type bridgeReply struct {
	State             usage.State    `json:"state"`
	Reason            string         `json:"reason"`
	Binding           string         `json:"binding"`
	ObservedAt        *time.Time     `json:"observed_at"`
	Windows           []usage.Window `json:"windows"`
	RetryAfterSeconds *int64         `json:"retry_after_seconds"`
}

func (a *Adapter) rpc(ctx context.Context, method string, input any) (bridgeReply, error) {
	body, err := json.Marshal(struct {
		Input any `json:"input"`
	}{input})
	if err != nil {
		return bridgeReply{}, fmt.Errorf("encode native quota request")
	}
	run := a.run
	if run == nil {
		run = usage.RunNativeCommandWithFiles
	}
	data, err := run(ctx, a.Command, []string{"api", "--standalone", "--log-level", "none", "POST", "/api/rpc/wi.quota/" + method, "--data", string(body)}, a.Environment, map[string][]byte{bridgePath: bridge})
	if err != nil {
		return bridgeReply{}, fmt.Errorf("native OpenCode quota bridge failed")
	}
	var envelope struct {
		Output *bridgeReply `json:"output"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || envelope.Output == nil || decoder.Decode(new(any)) != io.EOF {
		return bridgeReply{}, fmt.Errorf("unexpected native quota response")
	}
	return *envelope.Output, nil
}

type resolved struct {
	adapter *Adapter
	account usage.Account
	binding string
	problem *bridgeReply
}

func (p *resolved) Account() usage.Account { return p.account }
func (p *resolved) ValidateCachedQuota(ctx context.Context, q usage.Quota) usage.Quota {
	if q.ObservedAt == nil {
		return q
	}
	r, err := p.adapter.rpc(ctx, "identity", struct{}{})
	if err == nil && p.binding != "" && r.State == usage.OK && r.Binding == p.binding {
		return q
	}
	q.State, q.Detail = usage.Unavailable, reasonMessage("account_changed")
	if err != nil {
		q.Detail = reasonMessage("native_bridge_failed")
	}
	q.Windows, q.ObservedAt, q.Credits, q.IncludedUsageAllowed = []usage.Window{}, nil, nil, nil
	q.PreventStaleFallback = true
	return q
}

func (p *resolved) Quota(ctx context.Context) usage.Quota {
	a := p.adapter
	q := usage.Quota{Account: p.account, State: usage.Unavailable, CheckedAt: a.now(), Windows: []usage.Window{}, PreventStaleFallback: true}
	var r bridgeReply
	if p.problem != nil {
		r = *p.problem
	} else {
		var err error
		r, err = a.rpc(ctx, "read", struct {
			Binding string `json:"binding"`
		}{p.binding})
		if err != nil {
			r = bridgeReply{State: usage.Unavailable, Reason: "native_bridge_failed"}
		}
	}
	if r.State == usage.OK && p.binding != "" && r.Binding == p.binding && validQuota(r, a.now()) {
		q.State, q.ObservedAt, q.Windows = usage.OK, r.ObservedAt, r.Windows
		q.PreventStaleFallback = false
		return usage.AgeObservation(q, a.now())
	}
	if r.State == usage.OK {
		r.Reason = "schema_changed"
		if r.Binding != p.binding {
			r.Reason = "account_changed"
		}
	}
	if r.State == usage.Unsupported || r.State == usage.ExpiredAuth {
		q.State = r.State
	}
	q.Detail = reasonMessage(r.Reason)
	// Only an explicitly rechecked identity may retain stale data after a
	// transient failure. Identity, ownership, schema, and entitlement failures
	// never expose a previously cached account's quota.
	q.PreventStaleFallback = !(p.binding != "" && r.Binding == p.binding &&
		(r.Reason == "query_failed" || r.Reason == "http_error" || r.Reason == "rate_limited"))
	if r.RetryAfterSeconds != nil && *r.RetryAfterSeconds > 0 {
		seconds := *r.RetryAfterSeconds
		if seconds > 86400 {
			seconds = 86400
		}
		next := a.now().Add(time.Duration(seconds) * time.Second)
		q.NextRefreshAt = &next
	}
	return q
}

func validQuota(r bridgeReply, now time.Time) bool {
	if r.ObservedAt == nil || r.ObservedAt.After(now.Add(time.Minute)) || r.ObservedAt.Before(now.Add(-time.Minute)) || len(r.Windows) != 3 {
		return false
	}
	for i, id := range []string{"five_hour", "weekly", "monthly"} {
		w := r.Windows[i]
		if w.ID != id || w.RemainingPercent == nil || math.IsNaN(*w.RemainingPercent) || math.IsInf(*w.RemainingPercent, 0) || *w.RemainingPercent < 0 || *w.RemainingPercent > 100 || w.Scope != nil || (i > 0 && w.ResetsAt == nil) {
			return false
		}
	}
	return true
}

func reasonMessage(reason string) string {
	switch reason {
	case "no_login":
		return "Sign in to OpenCode Console with OpenCode v2."
	case "oauth_required":
		return "OpenCode Go quota requires the selected native Console OAuth connection; API keys are not substituted."
	case "client_version":
		return "The quota bridge requires OpenCode v2."
	case "console_origin":
		return "The quota bridge supports only the official OpenCode Console origin."
	case "identity_missing":
		return "OpenCode did not identify the selected Console account/workspace."
	case "account_changed":
		return "The selected OpenCode account or workspace changed; quota was discarded."
	case "workspace_mismatch", "owner_mismatch":
		return "Go subscription identity did not match the selected OpenCode account/workspace; quota was discarded."
	case "no_subscription":
		return "No Go subscription exists in the selected OpenCode workspace."
	case "access_paused":
		return "Go subscription access is paused in the selected OpenCode workspace."
	case "authentication_rejected":
		return "Console rejected native authentication. Sign in again with OpenCode."
	case "rate_limited":
		return "Console quota lookup was rate limited; no retry was attempted."
	case "plan_unknown", "schema_changed":
		return "OpenCode Console returned an unsupported Go quota schema."
	case "query_failed", "http_error":
		return "OpenCode Console quota lookup failed or timed out."
	default:
		return "Native OpenCode quota bridge unavailable. OpenCode v2 with local plugins enabled is required."
	}
}
