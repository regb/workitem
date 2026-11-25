package model

import (
	"context"
	"time"
)

// Event is a fact in an item's history: what happened, and who did it, for
// whom and why (see Origin).
type Event struct {
	Time  time.Time      `json:"time"`
	Type  string         `json:"type"`
	Actor string         `json:"actor"`
	For   string         `json:"for,omitempty"`
	Cause string         `json:"cause,omitempty"`
	Data  map[string]any `json:"data"`
}

// NewEvent records something wi observed or did on its own (actor "wi"), or
// a fact with an explicit actor.
func NewEvent(now time.Time, typ, actor string, data map[string]any) Event {
	if data == nil {
		data = map[string]any{}
	}
	return Event{Time: now, Type: typ, Actor: actor, Data: data}
}

// NewEventFrom records what the action ctx belongs to did, as its origin:
// the operator when the daemon acts with no caller.
func NewEventFrom(ctx context.Context, now time.Time, typ string, data map[string]any) Event {
	origin := ActingOrigin(ctx)
	event := NewEvent(now, typ, origin.Actor, data)
	event.For, event.Cause = origin.For, origin.Cause
	return event
}
