package daemonapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

const (
	// blockerTick is how often the daemon looks for agents stopped on a usage
	// limit, usage blockers to clear and follow-ups to send.
	blockerTick = 5 * time.Second
	// usageRetry is when a usage blocker is retried if the provider's usage
	// shows no exhausted window, so its reset is unknown: the data may be
	// stale, or the limit is one the provider does not report.
	usageRetry = 15 * time.Minute
	// usageUnknownRetry is when a usage blocker is retried if no usage check
	// reports its provider: without the usage plugin, or for a provider it
	// does not know. Each retry costs at most one refused prompt.
	usageUnknownRetry = time.Hour
	// followUpClient is the client the daemon sends follow-ups as when no
	// client holds control.
	followUpClient = "wi-follow-up"
)

// blockerLoop runs the daemon's blocker automation.
func (s *Server) blockerLoop() {
	defer s.callbacks.Done()
	ticker := time.NewTicker(blockerTick)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.runBlockers(time.Now().UTC())
		}
	}
}

// runBlockers sets usage blockers on agents stopped on a usage limit, clears
// the ones whose quota is back, and sends the follow-ups of items that no
// longer wait.
func (s *Server) runBlockers(now time.Time) {
	database := s.Core.Database()
	manifests, err := database.ListManifests()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wi: list items for blockers: %v\n", err)
		return
	}
	observations, err := database.ListProjection(coordinator.AgentObservationProjection)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wi: read agent observations: %v\n", err)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
	defer cancel()
	usageBlocked := false
	for _, m := range manifests {
		if m.State != model.StateActive {
			continue
		}
		var observation coordinator.AgentObservation
		if raw, ok := observations[m.ID]; ok {
			_ = json.Unmarshal(raw, &observation)
		}
		usage := usageBlocker(m)
		switch {
		case usage != nil:
			s.clearUsageBlocker(ctx, m, *usage, now)
			usageBlocked = true
		case len(m.FollowUps) > 0 && !m.Waiting(now):
			// Before a new usage blocker: the agent still shows the failure
			// its follow-up is about to continue.
			s.sendFollowUps(ctx, m, observation)
		case observation.Status == "problem" && observation.FailureReason == "usage_limit":
			s.blockOnUsage(ctx, m, observation, now)
			usageBlocked = true
		}
	}
	s.usage.blocked.Store(usageBlocked)
}

// usageBlocker is the item's usage blocker, if it has one.
func usageBlocker(m model.Manifest) *model.Blocker {
	for _, b := range m.Blockers {
		if b.Kind == model.BlockerUsage {
			return &b
		}
	}
	return nil
}

// blockOnUsage makes an item whose agent stopped on a usage limit wait for
// its provider's quota. The command ID names the failure, so a usage
// blocker the operator cleared is not set again for the same failure.
func (s *Server) blockOnUsage(ctx context.Context, m model.Manifest, observation coordinator.AgentObservation, now time.Time) {
	failedAt := now
	if at := observation.Activity.LastCompletedAt; at != nil {
		failedAt = *at
	} else if at := observation.LastActivityAt; at != nil {
		failedAt = *at
	}
	provider := s.itemUsageProvider(ctx, m)
	blocker := model.Blocker{Kind: model.BlockerUsage, Provider: provider, FollowUp: s.Config.Agent.UsageLimitMessage}
	retry := now.Add(usageUnknownRetry)
	if quota := s.cachedQuota(provider); quota != nil {
		retry = now.Add(usageRetry)
		if _, reset := quotaAvailability(quota); reset != nil && reset.After(now) {
			retry = *reset
		}
	}
	blocker.Until = &retry
	revision, err := s.canonical(ctx, m.ID, 0)
	if err != nil {
		return
	}
	command := coordinator.ManifestCommand{ID: "usage-limit-" + m.ID + "-" + strconv.FormatInt(failedAt.UnixNano(), 10), ProtocolVersion: coordinator.ProtocolVersion, Type: coordinator.CommandBlockerAdd, ItemID: m.ID, ExpectedRevision: &revision.Revision, Actor: "user", Blockers: []model.Blocker{blocker}, CreatedAt: now}
	if _, err := s.Local.ExecuteManifestCommand(ctx, command); err != nil && !strings.Contains(err.Error(), "already used") && !strings.Contains(err.Error(), "revision conflict") {
		fmt.Fprintf(os.Stderr, "wi: set usage blocker on %s: %v\n", m.ID, err)
	}
}

// clearUsageBlocker clears a usage blocker once its provider allows work
// again. A blocker with a reset time waits for it and then for a usage
// check made after it; one without waits for any check made after it was
// set. A blocker whose provider reports no usage waits for its reset time,
// or for the operator when it has none.
func (s *Server) clearUsageBlocker(ctx context.Context, m model.Manifest, blocker model.Blocker, now time.Time) {
	if blocker.Until != nil && blocker.Until.After(now) {
		return
	}
	since := blocker.CreatedAt
	if blocker.Until != nil {
		since = *blocker.Until
	}
	cause := ""
	quota, checkedAt, checked := s.cachedQuotaAt(blocker.Provider)
	switch {
	case !checked:
		// The daemon has not checked usage since it started.
		s.usage.mu.Lock()
		s.startUsageCheck(false)
		s.usage.mu.Unlock()
		return
	case quota == nil && blocker.Until != nil:
		cause = coordinator.BlockerClearedExpired
	case quota == nil:
		return
	case checkedAt.Before(since):
		// Ask for a check now rather than at the next interval.
		s.usage.mu.Lock()
		s.startUsageCheck(true)
		s.usage.mu.Unlock()
		return
	default:
		if available, _ := quotaAvailability(quota); !available {
			return
		}
		cause = coordinator.BlockerClearedUsageAvailable
	}
	revision, err := s.canonical(ctx, m.ID, 0)
	if err != nil {
		return
	}
	command := coordinator.ManifestCommand{ID: "usage-clear-" + blocker.ID, ProtocolVersion: coordinator.ProtocolVersion, Type: coordinator.CommandBlockerClear, ItemID: m.ID, ExpectedRevision: &revision.Revision, Actor: "user", BlockerID: blocker.ID, ClearCause: cause, CreatedAt: now}
	if _, err := s.Local.ExecuteManifestCommand(ctx, command); err != nil && !strings.Contains(err.Error(), "revision conflict") && !strings.Contains(err.Error(), "has no blocker") {
		fmt.Fprintf(os.Stderr, "wi: clear usage blocker on %s: %v\n", m.ID, err)
	}
}

// sendFollowUps sends an item's follow-ups, in the order their blockers
// cleared, as messages from the operator. A follow-up whose item was
// prompted after its blocker was set is dropped: the operator has moved on.
func (s *Server) sendFollowUps(ctx context.Context, m model.Manifest, observation coordinator.AgentObservation) {
	requested := observation.Activity.LastRequestedAt
	if activity, err := s.Local.AttentionActivity(ctx, m.ID); err == nil {
		requested = laterOf(requested, activity.Activity.LastRequestedAt)
	}
	for _, followUp := range m.FollowUps {
		outcome, reason := coordinator.FollowUpSent, ""
		if requested != nil && requested.After(followUp.BlockedAt) {
			outcome, reason = coordinator.FollowUpDropped, "the item was prompted after the blocker was set"
		} else if err := s.sendFollowUp(ctx, m.ID, followUp); err != nil {
			if connect.CodeOf(err) == connect.CodeUnavailable || ctx.Err() != nil {
				return
			}
			outcome, reason = coordinator.FollowUpDropped, err.Error()
			fmt.Fprintf(os.Stderr, "wi: send follow-up %s to %s: %v\n", followUp.ID, m.ID, err)
		}
		revision, err := s.canonical(ctx, m.ID, 0)
		if err != nil {
			return
		}
		command := coordinator.ManifestCommand{ID: "follow-up-" + followUp.ID, ProtocolVersion: coordinator.ProtocolVersion, Type: coordinator.CommandFollowUpDone, ItemID: m.ID, ExpectedRevision: &revision.Revision, Actor: "user", FollowUpID: followUp.ID, FollowUpOutcome: outcome, Reason: reason, CreatedAt: time.Now().UTC()}
		if _, err := s.Local.ExecuteManifestCommand(ctx, command); err != nil {
			fmt.Fprintf(os.Stderr, "wi: record follow-up %s of %s: %v\n", followUp.ID, m.ID, err)
			return
		}
	}
}

// sendFollowUp sends one follow-up as the dashboard sends a message,
// starting the agent when it is not running. It acts for the client that
// holds control, if one does, so a follow-up never displaces it. The message
// ID makes a repeated attempt deliver it once.
func (s *Server) sendFollowUp(ctx context.Context, itemID string, followUp model.FollowUp) error {
	who := identity{client: followUpClient, holder: "wi", actor: "follow-up"}
	if lease, err := s.Local.Lease(ctx, itemID); err == nil && lease != nil {
		who.client, who.holder = lease.ClientID, lease.Holder
	} else {
		// Control taken to send is given back at once, so no client finds
		// the item controlled by the daemon.
		defer func() {
			if lease, err := s.Local.Lease(ctx, itemID); err == nil && lease != nil && lease.ClientID == followUpClient {
				expected := lease.ID
				_, _ = s.Local.ReleaseLease(ctx, model.LeaseRequest{ItemID: itemID, Holder: lease.Holder, ClientID: lease.ClientID, ExpectedLeaseID: &expected})
			}
		}()
	}
	ctx = context.WithValue(ctx, identityKey{}, who)
	_, err := s.SendMessage(ctx, connect.NewRequest(&v1.SendMessageRequest{ItemId: itemID, Text: followUp.Message, ClientMessageId: "follow-up-" + followUp.ID}))
	return err
}

// itemUsageProvider is the usage provider an item's agent draws on: Claude
// Code's own, or for Pi the provider of its model, such as codex for
// openai-codex/gpt-6. Empty when it cannot tell.
func (s *Server) itemUsageProvider(ctx context.Context, m model.Manifest) string {
	settings, err := s.App.AgentSettings(ctx, resolve(m.ID))
	if err != nil {
		return ""
	}
	modelName := settings.Running["model"]
	if modelName == "" {
		modelName = settings.Values["model"]
	}
	return usageProvider(settings.Driver, modelName)
}

func usageProvider(driver, modelName string) string {
	if driver == "claude" {
		return "claude"
	}
	prefix, _, ok := strings.Cut(modelName, "/")
	if !ok || prefix == "" {
		return ""
	}
	switch prefix {
	case "anthropic":
		return "claude"
	case "openai-codex":
		return "codex"
	}
	return prefix
}

// cachedQuota is the last usage result for a provider, if usage checks
// report it.
func (s *Server) cachedQuota(provider string) *v1.UsageQuota {
	quota, _, _ := s.cachedQuotaAt(provider)
	return quota
}

// cachedQuotaAt is the last usage result for a provider and when it was
// checked, and whether usage was checked at all. A quota whose check failed
// counts as unknown.
func (s *Server) cachedQuotaAt(provider string) (*v1.UsageQuota, time.Time, bool) {
	u := &s.usage
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.result == nil {
		return nil, time.Time{}, false
	}
	if provider == "" {
		return nil, time.Time{}, true
	}
	for _, quota := range u.result.Quotas {
		if quota.Provider == provider && quota.ErrorCode == "" && !quota.Stale {
			return quota, u.checkedAt, true
		}
	}
	return nil, time.Time{}, true
}

// quotaAvailability says whether a quota allows work, and when the last of
// its exhausted windows resets.
func quotaAvailability(quota *v1.UsageQuota) (bool, *time.Time) {
	available := quota.IncludedUsageAllowed == nil || *quota.IncludedUsageAllowed
	var reset *time.Time
	for _, window := range quota.Windows {
		if window.RemainingPercent == nil || *window.RemainingPercent > 0 {
			continue
		}
		available = false
		if window.ResetsAt != nil {
			at := window.ResetsAt.AsTime()
			if reset == nil || at.After(*reset) {
				reset = &at
			}
		}
	}
	return available, reset
}

func laterOf(a, b *time.Time) *time.Time {
	if a == nil || b != nil && b.After(*a) {
		return b
	}
	return a
}
