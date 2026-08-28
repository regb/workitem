package view

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/priority"
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
type ResolveActivity func(string, Observation) (Activity, []string)

type AttentionCandidate struct {
	Item     Item     `json:"item"`
	Activity Activity `json:"activity"`
	Rank     int      `json:"rank"`
}
type QueueResult struct {
	Strategy   string               `json:"strategy"`
	Candidates []AttentionCandidate `json:"candidates"`
	Warnings   []string             `json:"warnings"`
}

func (s *Service) Enrich(ctx context.Context, result *Result, observe Observe, activity ResolveActivity, markers Markers, strategy string) []string {
	warnings := []string{}
	targets := make([]*Item, 0, len(result.Sections.Working)+len(result.Sections.Waiting))
	collect := func(items []Item) {
		for i := range items {
			if items[i].State == model.StateWorking || items[i].State == model.StateWaiting {
				targets = append(targets, &items[i])
			}
		}
	}
	collect(result.Sections.Working)
	collect(result.Sections.Waiting)
	type observationResult struct {
		status Observation
		err    error
	}
	observations := make([]observationResult, len(targets))
	jobs := make(chan int)
	workers := len(targets)
	if workers > 8 {
		workers = 8
	}
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				observations[index].status, observations[index].err = observe(ctx, targets[index].ID)
			}
		}()
	}
	for index := range targets {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	for index, item := range targets {
		status, err := observations[index].status, observations[index].err
		if err != nil {
			item.Agent = &AgentStatus{Status: "problem", Label: "problem", Marker: markers.Problem, Bucket: "needs_fixing", Reason: err.Error()}
			warnings = append(warnings, fmt.Sprintf("agent status for %s: %s", item.ID, err))
			continue
		}
		agentStatus := agentProjection(status, markers)
		item.Agent = &agentStatus
		item.Worktree = status.Worktree
		warnings = append(warnings, status.Warnings...)
		if item.State == model.StateWorking && agentStatus.Bucket == "needs_attention" && (item.Worktree == nil || item.Worktree.Status != "problem") {
			value, extra := activity(item.ID, status)
			item.Attention = &value
			warnings = append(warnings, extra...)
		}
	}
	candidates := []priority.Candidate{}
	for i, item := range result.Sections.Working {
		if item.Attention != nil {
			candidates = append(candidates, priority.Candidate{ID: item.ID, DeepWork: item.DeepWork, BaseOrder: i, StateChangedAt: item.ChangedAt, LastRequestedAt: item.Attention.LastRequestedAt, LastCompletedAt: item.Attention.LastCompletedAt, LastDeferredAt: item.Attention.LastDeferredAt})
		}
	}
	ranked, err := priority.Rank(strategy, candidates)
	if err != nil {
		warnings = append(warnings, err.Error())
		ranked = candidates
	}
	ranks := map[string]int{}
	for i, c := range ranked {
		ranks[c.ID] = i + 1
	}
	for i := range result.Sections.Working {
		result.Sections.Working[i].AttentionRank = ranks[result.Sections.Working[i].ID]
	}
	return warnings
}

func Queue(result Result, strategy string) QueueResult {
	candidates := []AttentionCandidate{}
	for _, item := range result.Sections.Working {
		if item.Attention != nil && item.AttentionRank > 0 {
			candidates = append(candidates, AttentionCandidate{Item: item, Activity: *item.Attention, Rank: item.AttentionRank})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Rank < candidates[j].Rank })
	if strings.TrimSpace(strategy) == "" {
		strategy = priority.DefaultStrategy
	}
	return QueueResult{Strategy: strategy, Candidates: candidates, Warnings: append([]string{}, result.Warnings...)}
}

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
