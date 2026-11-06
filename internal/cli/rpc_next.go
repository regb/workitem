package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
)

// nextOptions are wi next's flags: label filters for the navigation ring and
// at most one action on the item being left.
type nextOptions struct {
	labels                                  []string
	noAgent                                 bool
	deferCurrent, waitCurrent, closeCurrent bool
	until, on, event, resolution            string
}

func (o nextOptions) validate() error {
	actions := 0
	for _, enabled := range []bool{o.deferCurrent, o.waitCurrent, o.closeCurrent} {
		if enabled {
			actions++
		}
	}
	if actions > 1 {
		return errors.New("pass only one of --defer, --wait, or --close")
	}
	conditions := 0
	for _, value := range []string{o.until, o.on, o.event} {
		if value != "" {
			conditions++
		}
	}
	if conditions > 0 && !o.waitCurrent {
		return errors.New("--until, --on, and --for require --wait")
	}
	if o.waitCurrent && conditions != 1 {
		return errors.New("--wait requires one of --until, --on, or --for")
	}
	if o.resolution != "" && !o.closeCurrent {
		return errors.New("--resolution requires --close")
	}
	if o.resolution != "" && !model.ValidResolution(o.resolution) {
		return fmt.Errorf("invalid resolution %q; expected completed, abandoned, or superseded", o.resolution)
	}
	return nil
}

// rpcNext moves to the next item needing attention, after the current one in
// the attention order, wrapping at the end. With --defer or --wait the item
// being left is deferred or made to wait first; with --close the client
// switches first and then closes the item it left, so the closed session is
// never the one being shown.
func rpcNext(ctx context.Context, cfg Config, o nextOptions, jsonOut bool) error {
	if err := o.validate(); err != nil {
		return usageErr{err}
	}
	defaults := []string{}
	if cfg.GlobalConfig != nil {
		defaults = cfg.GlobalConfig.List.Labels
	}
	rules, err := effectiveListLabelRules(defaults, cfg.Env, o.labels)
	if err != nil {
		return usageErr{err}
	}
	items, err := cfg.API.Items.ListItems(ctx, rpcRequest(cfg, &v1.ListItemsRequest{Order: v1.ListOrder_LIST_ORDER_ATTENTION, LabelRules: apiLabelRules(rules), Inspect: !o.noAgent}))
	if err != nil {
		return err
	}
	current, _ := rpcSelect(ctx, cfg, "")
	acting := o.deferCurrent || o.waitCurrent || o.closeCurrent
	if acting && current == "" {
		return errors.New("no current work item is selected")
	}
	ring := []string{}
	for _, row := range items.Msg.Items {
		if row.Observation.GetNeedsAttention() {
			ring = append(ring, row.Id)
		}
	}
	selected, wrapped, inQueue := nextInRing(ring, current)

	if o.closeCurrent {
		if selected == "" {
			return errors.New("no other item needs attention; close this item with wi close")
		}
		// Refuse before switching, so a refused close leaves the operator
		// where they were.
		if err := closeAllowed(ctx, cfg, current); err != nil {
			return err
		}
	}
	if o.deferCurrent {
		if _, err := cfg.API.Items.DeferItem(ctx, rpcRequest(cfg, &v1.DeferItemRequest{ItemId: current})); err != nil {
			return err
		}
		if !jsonOut {
			fmt.Fprintf(cfg.Stdout, "deferred current item: %s\n", current)
		}
	}
	if o.waitCurrent {
		request, err := rpcBlockerRequest(ctx, cfg, current, o.until, o.on, o.event)
		if err != nil {
			return err
		}
		if request == nil {
			// The operator cancelled choosing the item to wait on.
			return nil
		}
		added, err := cfg.API.Items.AddBlocker(ctx, rpcRequest(cfg, request))
		if err != nil {
			return err
		}
		if !jsonOut {
			fmt.Fprintf(cfg.Stdout, "current item %s is waiting (blocker %s)\n", current, added.Msg.GetBlocker().GetId())
		}
	}
	if selected == "" {
		if !jsonOut {
			fmt.Fprintln(cfg.Stdout, "no other item needs attention")
		}
		return nil
	}
	if !jsonOut {
		fmt.Fprintf(cfg.Stdout, "switched to next NEEDS ATTENTION item: %s\n", selected)
		if wrapped {
			fmt.Fprintln(cfg.Stdout, "wrapped to the top of NEEDS ATTENTION")
		} else if !inQueue && current != "" {
			fmt.Fprintln(cfg.Stdout, "current item is outside NEEDS ATTENTION; started at the top")
		}
	}
	if err := rpcOpenItem(ctx, cfg, selected, jsonOut); err != nil {
		return err
	}
	if o.closeCurrent {
		resolution := o.resolution
		if resolution == "" {
			resolution = string(model.ResolutionCompleted)
		}
		closed, err := cfg.API.Lifecycle.CloseItem(ctx, rpcRequest(cfg, &v1.CloseItemRequest{ItemId: current, Resolution: v1.Resolution(v1.Resolution_value["RESOLUTION_"+strings.ToUpper(resolution)]), ConfirmAttachedTerminal: true}))
		if err != nil {
			return fmt.Errorf("switched, but closing %s failed: %w", current, err)
		}
		if _, err := rpcWaitJob(ctx, cfg, closed.Msg.Job, true); err != nil {
			return fmt.Errorf("switched, but closing %s failed: %w", current, err)
		}
		if !jsonOut {
			fmt.Fprintf(cfg.Stdout, "closed previous item: %s\n", current)
		}
	}
	return nil
}

// nextInRing picks the item after current in the attention ring, never
// current itself. wrapped reports passing the end; inQueue whether current
// was in the ring at all. An empty result means no other item qualifies.
func nextInRing(ring []string, current string) (selected string, wrapped, inQueue bool) {
	at := -1
	for i, id := range ring {
		if id == current {
			at = i
			break
		}
	}
	if at < 0 {
		if len(ring) > 0 {
			return ring[0], false, false
		}
		return "", false, false
	}
	if len(ring) < 2 {
		return "", false, true
	}
	next := (at + 1) % len(ring)
	return ring[next], next == 0, true
}

// closeAllowed refuses a close that would fail after switching away: a busy
// agent, or uncommitted changes that are not just copies of attachments.
func closeAllowed(ctx context.Context, cfg Config, id string) error {
	agent, err := cfg.API.Agents.GetAgent(ctx, rpcRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
	if err != nil {
		return err
	}
	if agent.Msg.GetAgent().GetStatus() == v1.AgentStatus_AGENT_STATUS_BUSY {
		return errors.New("the current agent is working; stop it first, or close with wi close --force")
	}
	changes, err := cfg.API.Workspaces.GetUncommittedChanges(ctx, rpcRequest(cfg, &v1.GetUncommittedChangesRequest{ItemId: id}))
	if err != nil {
		return err
	}
	if files := changes.Msg.GetFiles(); len(files) > 0 {
		return fmt.Errorf("the current checkout has uncommitted changes (%s); commit them or close with wi close --commit", strings.Join(files, ", "))
	}
	return nil
}

// rpcOpenItem shows an item's agent in its terminal, starting the agent
// without a message when it is not running.
func rpcOpenItem(ctx context.Context, cfg Config, id string, jsonOut bool) error {
	view, err := cfg.API.Agents.GetAgent(ctx, terminalRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
	if err != nil {
		return err
	}
	if !view.Msg.GetAgent().GetOnline() {
		started, err := cfg.API.Lifecycle.StartItem(ctx, rpcRequest(cfg, &v1.StartItemRequest{ItemId: id, Start: &v1.StartOptions{Mode: v1.AgentMode_AGENT_MODE_TUI, SendNothing: true}}))
		if err != nil {
			return err
		}
		if _, err := rpcWaitJob(ctx, cfg, started.Msg.Job, jsonOut); err != nil {
			return err
		}
	}
	return rpcEnterTerminal(ctx, cfg, id, false, jsonOut)
}
