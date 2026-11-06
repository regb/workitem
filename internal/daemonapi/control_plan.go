package daemonapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	"github.com/regb/workitem/internal/app"
)

// Renewal timestamps are deliberately excluded: extending the same lease does
// not change what confirmation authorizes. Starting an offline agent is not an
// interruption; displaced ownership, runtime identity and effects are bound.
func (s *Server) planToken(ctx context.Context, status app.ControlStatus, plan app.ControlPlan, client string) string {
	leaseID := ""
	if status.Lease != nil {
		leaseID = status.Lease.ID
	}
	manifest, _ := s.App.Store.LoadManifest(status.WorkItemID)
	profiles, _ := s.Local.AgentProfiles(ctx)
	data, _ := json.Marshal([]any{manifest.State, manifest.Agent, manifest.Checkout.Claim, profiles.Revision, status.WorkItemID, client, caller(ctx).holder, status.RuntimeID, status.RuntimeOnline, status.RuntimeMode, status.Channels, status.Busy, status.Implicit, status.TerminalClients, leaseID, plan.NeedsConfirmation, plan.DisplacesTerminal, plan.Abort, plan.Replace, plan.Unavailable})
	mac := hmac.New(sha256.New, s.planSecret[:])
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) validatePlan(ctx context.Context, token string, status app.ControlStatus, plan app.ControlPlan, client string) error {
	if token == "" || !hmac.Equal([]byte(token), []byte(s.planToken(ctx, status, plan, client))) {
		return aborted("control plan changed; read and confirm the new plan")
	}
	return nil
}
