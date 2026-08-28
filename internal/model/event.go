package model

import "time"

type Event struct {
	Time  time.Time      `json:"time"`
	Type  string         `json:"type"`
	Actor string         `json:"actor"`
	Data  map[string]any `json:"data"`
}

func NewEvent(now time.Time, typ, actor string, data map[string]any) Event {
	if data == nil {
		data = map[string]any{}
	}
	return Event{Time: now, Type: typ, Actor: actor, Data: data}
}
