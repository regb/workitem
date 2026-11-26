package view

import (
	"context"
)

type Observation struct {
	Status                 string
	Reason                 string
	ProcessOnline          bool
	TurnState              string
	LastActivityAgeSeconds int64
	Worktree               *WorktreeStatus
	Activity               Activity
	Warnings               []string
}
type Observe func(context.Context, string) (Observation, error)
