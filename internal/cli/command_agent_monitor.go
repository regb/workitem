package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
)

type agentRuntimeEventsResult struct {
	WorkItemID string               `json:"work_item_id"`
	RuntimeID  string               `json:"runtime_id,omitempty"`
	Events     []agent.RuntimeEvent `json:"events"`
	Warnings   []string             `json:"warnings"`
}

func runAgentMonitor(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("agent monitor", cfg.Stderr)
	var item string
	var limit int
	var follow bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.IntVar(&limit, "limit", 50, "show the latest N runtime events; zero means all")
	fs.BoolVar(&follow, "follow", false, "continue streaming new runtime events")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	if limit < 0 {
		return usageErr{errors.New("--limit must be nonnegative")}
	}
	resolve := app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}
	manifest, err := cfg.App.ResolveItem(ctx, resolve)
	if err != nil {
		return err
	}
	return runCoordinatorAgentMonitor(ctx, cfg, manifest.ID, limit, follow, jsonOut)
}

func runCoordinatorAgentMonitor(ctx context.Context, cfg Config, itemID string, limit int, follow, jsonOut bool) error {
	request := coordinator.RuntimeEventsRequest{ItemID: itemID, Limit: limit}
	result, err := cfg.Coordinator.RuntimeEvents(ctx, request)
	if err != nil {
		return err
	}
	events := coordinatorRuntimeEvents(result.Events)
	runtimeID := ""
	if len(events) > 0 {
		runtimeID = events[len(events)-1].RuntimeID
	}
	if !follow {
		response := agentRuntimeEventsResult{WorkItemID: itemID, RuntimeID: runtimeID, Events: events, Warnings: []string{}}
		if jsonOut {
			return writeJSON(cfg.Stdout, response)
		}
		printAgentRuntimeEvents(cfg, response, false)
		return nil
	}
	if !jsonOut {
		fmt.Fprintf(cfg.Stdout, "work item: %s\n", itemID)
		if runtimeID != "" {
			fmt.Fprintf(cfg.Stdout, "runtime: %s\n", runtimeID)
		}
	}
	for _, event := range events {
		if err := printAgentRuntimeEvent(cfg, event, jsonOut); err != nil {
			return err
		}
	}
	after := result.LastSequence
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		result, err = cfg.Coordinator.RuntimeEvents(ctx, coordinator.RuntimeEventsRequest{ItemID: itemID, AfterSequence: after, Limit: 1000, WaitMillis: 30000})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, event := range coordinatorRuntimeEvents(result.Events) {
			if event.RuntimeID != "" && event.RuntimeID != runtimeID {
				runtimeID = event.RuntimeID
				if !jsonOut {
					fmt.Fprintf(cfg.Stdout, "runtime: %s\n", runtimeID)
				}
			}
			if err := printAgentRuntimeEvent(cfg, event, jsonOut); err != nil {
				return err
			}
		}
		if result.LastSequence > after {
			after = result.LastSequence
		}
	}
}

func coordinatorRuntimeEvents(events []coordinator.DomainEvent) []agent.RuntimeEvent {
	result := make([]agent.RuntimeEvent, 0, len(events))
	for _, event := range events {
		var payload struct {
			RuntimeID string `json:"runtime_id"`
			RequestID string `json:"request_id"`
			Role      string `json:"role"`
			ToolName  string `json:"tool_name"`
		}
		_ = json.Unmarshal(event.Payload, &payload)
		result = append(result, agent.RuntimeEvent{ProtocolVersion: agent.RuntimeProtocolVersion, EventID: event.ID, RuntimeID: payload.RuntimeID, WorkItemID: event.ItemID, Type: event.Type, Timestamp: event.Timestamp, RequestID: payload.RequestID, Role: payload.Role, ToolName: payload.ToolName})
	}
	return result
}

func printAgentRuntimeEvents(cfg Config, res agentRuntimeEventsResult, jsonOut bool) {
	if !jsonOut {
		fmt.Fprintf(cfg.Stdout, "work item: %s\n", res.WorkItemID)
		if res.RuntimeID != "" {
			fmt.Fprintf(cfg.Stdout, "runtime: %s\n", res.RuntimeID)
		}
	}
	for _, event := range res.Events {
		_ = printAgentRuntimeEvent(cfg, event, jsonOut)
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
}

func printAgentRuntimeEvent(cfg Config, event agent.RuntimeEvent, jsonOut bool) error {
	if jsonOut {
		return writeJSON(cfg.Stdout, event)
	}
	fmt.Fprintf(cfg.Stdout, "%s  %-20s", event.Timestamp.Format(timeFormatHuman), event.Type)
	if event.ToolName != "" {
		fmt.Fprintf(cfg.Stdout, " %s", event.ToolName)
	}
	if event.Text != "" {
		fmt.Fprintf(cfg.Stdout, " %s", truncateRunes(strings.ReplaceAll(event.Text, "\n", " "), 120))
	}
	if event.Error != "" {
		fmt.Fprintf(cfg.Stdout, " error=%s", event.Error)
	}
	fmt.Fprintln(cfg.Stdout)
	return nil
}
