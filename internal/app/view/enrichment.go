package view

import (
	"context"
)

type Markers struct{ Busy, Idle, Problem string }
type Observation struct {
	Status                 string
	Reason                 string
	ProcessOnline          bool
	TurnState              string
	LastActivityAgeSeconds int64
	Worktree               *WorktreeStatus
	Activity               Activity
	ActivityWarnings       []string
	Warnings               []string
}
type Observe func(context.Context, string) (Observation, error)

func ProjectAgent(status Observation, markers Markers) AgentStatus {
	return agentProjection(status, markers)
}

func agentProjection(status Observation, markers Markers) AgentStatus {
	label := status.Status
	if label == "" {
		label = "problem"
	}
	bucket := "needs_attention"
	marker := markers.Idle
	switch status.Status {
	case "problem", "":
		bucket = "needs_fixing"
		marker = markers.Problem
	case "busy":
		bucket = "in_progress"
		marker = markers.Busy
	}
	return AgentStatus{Status: status.Status, Label: label, Marker: marker, Bucket: bucket, Reason: status.Reason, ProcessOnline: status.ProcessOnline, TurnState: status.TurnState, LastActivityAgeSeconds: status.LastActivityAgeSeconds}
}
