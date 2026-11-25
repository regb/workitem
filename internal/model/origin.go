package model

import (
	"context"
	"fmt"
	"strings"
)

// Origin is where an action came from: the principal that acted, the user
// whose authority it ran under, and what caused it. The daemon sets it from
// what it verified (a caller's credential, a schedule firing, a blocker
// clearing), never from what a client claims, and records it with what the
// action did.
type Origin struct {
	// Actor is the principal that acted: "user:<id>" or "item:<id>".
	Actor string `json:"actor,omitempty"`
	// For is the user whose authority it ran under: the actor itself for a
	// user, the end of an item's owner chain for an item's agent.
	For string `json:"for,omitempty"`
	// Cause links what led to it, when not a caller's own request:
	// "schedule:<id>", "blocker:<id>", "proposal:<id>".
	Cause string `json:"cause,omitempty"`
}

// OperatorUser is the one user an installation has.
const OperatorUser = "user:operator"

// SystemActor names what the daemon records on its own, such as runtime
// observations; it is not a principal and grants nothing.
const SystemActor = "wi"

// ItemPrincipal names an item's agent.
func ItemPrincipal(itemID string) string { return "item:" + itemID }

// PrincipalItem is the item a principal names, or "".
func PrincipalItem(principal string) string {
	if id, ok := strings.CutPrefix(principal, "item:"); ok && ValidID(id) {
		return id
	}
	return ""
}

// ValidPrincipal accepts the principals an action can be recorded as.
func ValidPrincipal(principal string) bool {
	if PrincipalItem(principal) != "" {
		return true
	}
	user, ok := strings.CutPrefix(principal, "user:")
	return ok && user != "" && len(user) <= 128 && !strings.ContainsAny(user, " \t\r\n")
}

// Validate refuses an origin naming something other than principals.
func (o Origin) Validate() error {
	if o.Actor != "" && !ValidPrincipal(o.Actor) {
		return fmt.Errorf("invalid actor %q", o.Actor)
	}
	if o.For != "" && (!ValidPrincipal(o.For) || PrincipalItem(o.For) != "") {
		return fmt.Errorf("an action runs for a user, not %q", o.For)
	}
	if len(o.Cause) > 200 || strings.ContainsAny(o.Cause, " \t\r\n") {
		return fmt.Errorf("invalid cause %q", o.Cause)
	}
	return nil
}

type originKey struct{}

// WithOrigin carries an action's origin through the work it starts.
func WithOrigin(ctx context.Context, origin Origin) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}

// OriginFrom is the origin of the action ctx belongs to. Work the daemon
// does on its own has none.
func OriginFrom(ctx context.Context) Origin {
	value, _ := ctx.Value(originKey{}).(Origin)
	return value
}

// ActorOr is the origin's actor, or fallback when the action has none.
func (o Origin) ActorOr(fallback string) string {
	if o.Actor != "" {
		return o.Actor
	}
	return fallback
}

// ActingOrigin is the origin of the action ctx belongs to, completed for
// recording: an action without a caller is the operator's, and a user acts
// for themselves.
func ActingOrigin(ctx context.Context) Origin {
	origin := OriginFrom(ctx)
	if origin.Actor == "" {
		origin.Actor = OperatorUser
	}
	if origin.For == "" && origin.Actor != SystemActor {
		if PrincipalItem(origin.Actor) == "" {
			origin.For = origin.Actor
		} else {
			origin.For = OperatorUser
		}
	}
	return origin
}

// ActorFrom is who acts in ctx, for commands that record only an actor.
func ActorFrom(ctx context.Context) string { return ActingOrigin(ctx).Actor }
