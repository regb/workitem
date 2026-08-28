package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func runAgentStatus(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("agent status", cfg.Stderr)
	var item string
	var all bool
	var staleAfter time.Duration
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.BoolVar(&all, "all", false, "show an overview for all working/waiting items")
	fs.DurationVar(&staleAfter, "stale-after", 10*time.Minute, "mark incomplete agent activity stale after this duration")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	if all {
		if selector != "" {
			return usageErr{errors.New("--all cannot be combined with --item or a positional item")}
		}
		return runAgentStatusOverview(ctx, cfg, jsonOut, staleAfter)
	}
	res, err := coordinatorAgentStatus(ctx, cfg, selector, staleAfter)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	printAgentStatus(cfg.Stdout, res)
	return nil
}

func runAgentStatusOverview(ctx context.Context, cfg Config, jsonOut bool, staleAfter time.Duration) error {
	list := cfg.App.WorkList(app.WorkListOptions{})
	items := append([]app.WorkListItem{}, list.Sections.Working...)
	items = append(items, list.Sections.Waiting...)
	statuses := []app.AgentStatusResult{}
	warnings := append([]string{}, list.Warnings...)
	type statusResult struct {
		status app.AgentStatusResult
		err    error
	}
	results := make([]statusResult, len(items))
	jobs := make(chan int)
	workers := len(items)
	if workers > 8 {
		workers = 8
	}
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				results[index].status, results[index].err = coordinatorAgentStatus(ctx, cfg, items[index].ID, staleAfter)
			}
		}()
	}
	for index := range items {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	for index, result := range results {
		if result.err != nil {
			warnings = append(warnings, fmt.Sprintf("agent status for %s: %s", items[index].ID, result.err))
			continue
		}
		statuses = append(statuses, result.status)
	}
	res := agentStatusOverviewResult{Statuses: statuses, Warnings: warnings}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	printAgentStatusOverview(cfg.Stdout, res)
	return nil
}

type agentStatusOverviewResult struct {
	Statuses []app.AgentStatusResult `json:"statuses"`
	Warnings []string                `json:"warnings"`
}

func printAgentStatusOverview(w io.Writer, res agentStatusOverviewResult) {
	if len(res.Statuses) == 0 {
		fmt.Fprintln(w, "no working/waiting work items with agent status; pass an item selector for a specific work item")
		return
	}
	ids := make([]string, 0, len(res.Statuses))
	for _, status := range res.Statuses {
		ids = append(ids, status.WorkItemID)
	}
	prefixes := model.UniqueIDPrefixes(ids, 6)
	fmt.Fprintln(w, "AGENT STATUS")
	fmt.Fprintf(w, "  %-10s %-14s %-10s %s\n", "ID", "STATUS", "WORKTREE", "REASON")
	for _, status := range res.Statuses {
		worktree := ""
		if status.Worktree != nil {
			worktree = status.Worktree.Status
		}
		fmt.Fprintf(w, "  %-10s %-14s %-10s %s\n", prefixes[status.WorkItemID], status.Status, worktree, truncateRunes(status.Reason, 96))
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

func printAgentStatus(w io.Writer, res app.AgentStatusResult) {
	fmt.Fprintf(w, "work item: %s\n", res.WorkItemID)
	fmt.Fprintf(w, "agent status: %s\n", res.Status)
	if res.Reason != "" {
		fmt.Fprintf(w, "reason: %s\n", res.Reason)
	}
	if res.Process.TmuxSession != "" {
		fmt.Fprintf(w, "tmux: %s:%s", res.Process.TmuxSession, res.Process.TmuxWindow)
		if res.Process.TmuxPaneID != "" {
			fmt.Fprintf(w, " pane %s", res.Process.TmuxPaneID)
		}
		if res.Process.TmuxPanePID != 0 {
			fmt.Fprintf(w, " pid %d", res.Process.TmuxPanePID)
		}
		fmt.Fprintln(w)
	}
	if res.Process.PiPID != 0 {
		fmt.Fprintf(w, "pi pid: %d\n", res.Process.PiPID)
	}
	fmt.Fprintf(w, "process online: %v\n", res.Process.Online)
	if res.PiSession.Path != "" {
		fmt.Fprintf(w, "pi session: %s\n", res.PiSession.Path)
	}
	if res.PiSession.InferredTurnState != "" {
		fmt.Fprintf(w, "turn: %s\n", res.PiSession.InferredTurnState)
	}
	if res.PiSession.LastEvent != nil {
		fmt.Fprintf(w, "last event: %s", res.PiSession.LastEvent.Type)
		if res.PiSession.LastEvent.Role != "" {
			fmt.Fprintf(w, " role=%s", res.PiSession.LastEvent.Role)
		}
		if !res.PiSession.LastEvent.Timestamp.IsZero() {
			fmt.Fprintf(w, " at %s", res.PiSession.LastEvent.Timestamp.Format(timeFormatHuman))
		}
		fmt.Fprintln(w)
	}
	if res.Worktree != nil {
		fmt.Fprintf(w, "worktree: %s", res.Worktree.Status)
		if res.Worktree.Reason != "" {
			fmt.Fprintf(w, " (%s)", res.Worktree.Reason)
		}
		fmt.Fprintln(w)
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}
