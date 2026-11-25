package daemonapi

import (
	"context"
	"time"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

// returnDelegations gives the unfinished items a closed item owned back to
// that item's owner, marked unread so they are seen: nothing an agent
// delegated is left without someone to follow it up. It runs after the
// close, locking each delegated item on its own, and reports nothing; a
// failure leaves the item with its closed owner, which its user can take.
func (s *Server) returnDelegations(closedID string) {
	if !s.startCallback() {
		return
	}
	go func() {
		defer s.callbacks.Done()
		ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
		defer cancel()
		closed, err := s.App.Store.LoadManifest(closedID)
		if err != nil || closed.State != model.StateClosed {
			return
		}
		ctx = model.WithOrigin(ctx, model.Origin{Actor: model.SystemActor, Cause: "closed:" + closedID})
		manifests, err := s.Local.ListManifests(ctx)
		if err != nil {
			return
		}
		for _, m := range manifests {
			if m.Owner != model.ItemPrincipal(closedID) || m.State == model.StateClosed {
				continue
			}
			unlock := s.lock(m.ID)
			_, err := s.command(ctx, m.ID, coordinator.CommandOwnerSet, 0, func(c *coordinator.ManifestCommand) { c.Owner = closed.OwnerPrincipal() })
			if err == nil {
				_, _ = s.App.MarkUnread(ctx, resolve(m.ID))
			}
			unlock()
		}
	}()
}

// manifestLookup finds items by ID, for walking owner chains.
func (s *Server) manifestLookup(ctx context.Context) func(string) (model.Manifest, bool) {
	return func(id string) (model.Manifest, bool) {
		c, err := s.Local.CanonicalManifest(ctx, id)
		return c.Manifest, err == nil
	}
}
