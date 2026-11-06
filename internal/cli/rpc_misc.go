package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
)

func rpcBlockers(ctx context.Context, command string, args []string, cfg Config, jsonOut bool) error {
	action := "add"
	if command == "blocker" {
		if len(args) == 0 {
			return usageErr{errors.New("blocker requires list, add or clear")}
		}
		action, args = args[0], args[1:]
	}
	if action != "add" && action != "list" && action != "clear" {
		return usageErr{fmt.Errorf("unknown blocker action %q", action)}
	}
	fs := newFlagSet(command+" "+action, cfg.Stderr)
	selected := fs.String("item", "", "item")
	kind := fs.String("kind", "", "time, item or external")
	until := fs.String("until", "", "absolute time or duration")
	on := fs.String("on", "", "blocking item")
	event := fs.String("for", "", "external event")
	fs.StringVar(event, "event", "", "external event")
	clear := fs.Bool("clear", false, "clear all blockers")
	all := fs.Bool("all", false, "clear all blockers")
	label := fs.String("label", "", "blocker label")
	blockerID := fs.String("blocker", "", "blocker id")
	fs.StringVar(blockerID, "id", "", "blocker id")
	var meta repeatFlag
	fs.Var(&meta, "meta", "KEY=VALUE")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return usageErr{errors.New("expected at most one item selector")}
	}
	clearing := *clear || action == "clear"
	if *all && !clearing {
		return usageErr{errors.New("--all requires blocker clear or wait --clear")}
	}
	if *all && *blockerID != "" {
		return usageErr{errors.New("pass either --id or --all, not both")}
	}
	if !clearing && *blockerID != "" {
		return usageErr{errors.New("--id requires clearing a blocker")}
	}
	if (clearing || action == "list") && (*kind != "" || *until != "" || *on != "" || *event != "" || *label != "" || len(meta) > 0) {
		return usageErr{errors.New("blocker creation flags cannot be used when listing or clearing")}
	}
	switch *kind {
	case "": // Keep inferred kinds for the wait command and older callers.
	case "time":
		if *until == "" {
			return usageErr{errors.New("time blockers require --until")}
		}
	case "item":
		if *on == "" {
			return usageErr{errors.New("item blockers require --on")}
		}
	case "external":
		if *event == "" {
			return usageErr{errors.New("external blockers require --event or --for")}
		}
	default:
		return usageErr{fmt.Errorf("unknown blocker kind %q", *kind)}
	}
	selector := *selected
	if fs.NArg() == 1 {
		if action == "clear" && *blockerID == "" && !*all {
			// Preserve the old positional blocker ID syntax. With --id or
			// --all, the positional argument is the documented item selector.
			*blockerID = fs.Arg(0)
		} else {
			if selector != "" {
				return usageErr{errors.New("pass an item selector or --item, not both")}
			}
			selector = fs.Arg(0)
		}
	}
	id, err := rpcSelect(ctx, cfg, selector)
	if err != nil {
		return err
	}
	if action == "list" {
		value, err := cfg.API.Items.GetItem(ctx, rpcRequest(cfg, &v1.GetItemRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	if clearing {
		value, err := cfg.API.Items.ClearBlocker(ctx, rpcRequest(cfg, &v1.ClearBlockerRequest{ItemId: id, BlockerId: *blockerID, ClearAll: *blockerID == ""}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	metadata, err := model.ParseBlockerMeta(meta)
	if err != nil {
		return usageErr{err}
	}
	r, err := rpcBlockerRequest(ctx, cfg, id, *until, *on, *event)
	if err != nil {
		return err
	}
	if r == nil {
		// The operator cancelled the picker for --on -.
		return nil
	}
	r.Label, r.Meta = *label, metadata
	value, err := cfg.API.Items.AddBlocker(ctx, rpcRequest(cfg, r))
	if err != nil {
		return err
	}
	return rpcEmit(cfg, jsonOut, value.Msg)
}

// rpcBlockerRequest builds the blocker for one wait condition: a time, an
// item (- picks it interactively, leaving out the current item), or an
// external event. It returns nil when the operator cancels the picker.
func rpcBlockerRequest(ctx context.Context, cfg Config, id, until, on, event string) (*v1.AddBlockerRequest, error) {
	conditions := 0
	for _, value := range []string{until, on, event} {
		if value != "" {
			conditions++
		}
	}
	if conditions != 1 {
		return nil, usageErr{errors.New("exactly one of --until, --on or --event/--for is required")}
	}
	r := &v1.AddBlockerRequest{ItemId: id}
	switch {
	case until != "":
		at, err := parseBlockUntil(time.Now(), until)
		if err != nil {
			return nil, usageErr{err}
		}
		r.Kind = v1.BlockerKind_BLOCKER_KIND_TIME
		r.Until = timestamp(at)
	case on == "-":
		candidate, ok, err := rpcPickItem(ctx, cfg, nil, false, false, true)
		if err != nil || !ok {
			return nil, err
		}
		r.Kind = v1.BlockerKind_BLOCKER_KIND_ITEM
		r.BlockingItemId = candidate.Item.ID
	case on != "":
		r.Kind = v1.BlockerKind_BLOCKER_KIND_ITEM
		blocking, err := rpcSelect(ctx, cfg, on)
		if err != nil {
			return nil, err
		}
		r.BlockingItemId = blocking
	case event != "":
		r.Kind = v1.BlockerKind_BLOCKER_KIND_EXTERNAL
		r.Event = event
	default:
		return nil, usageErr{errors.New("one of --until, --on or --for is required")}
	}
	return r, nil
}
func rpcAttention(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("attention requires queue, activity or defer")}
	}
	if args[0] == "queue" {
		return rpcList(ctx, args[1:], cfg, jsonOut)
	}
	id, err := rpcItemArgs(ctx, cfg, "attention "+args[0], args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "activity":
		value, err := cfg.API.Items.GetItem(ctx, rpcRequest(cfg, &v1.GetItemRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	case "read", "unread":
		value, err := cfg.API.Items.MarkRead(ctx, rpcRequest(cfg, &v1.MarkReadRequest{ItemId: id, Unread: args[0] == "unread"}))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		fmt.Fprintf(cfg.Stdout, "%s: marked %s\n", id, args[0])
		return nil
	case "defer":
		value, err := cfg.API.Items.DeferItem(ctx, rpcRequest(cfg, &v1.DeferItemRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	return usageErr{fmt.Errorf("unknown attention action %q", args[0])}
}
func rpcSystem(ctx context.Context, command string, args []string, cfg Config, jsonOut bool) error {
	switch command {
	case "info", "version":
		return rpcInfo(ctx, command, args, cfg, jsonOut)
	case "config":
		return rpcConfig(ctx, args, cfg, jsonOut)
	case "shutdown":
		fs := newFlagSet("shutdown", cfg.Stderr)
		force := fs.Bool("force", false, "stop busy agents")
		if err := rpcParse(fs, args); err != nil {
			return err
		}
		value, err := cfg.API.System.Shutdown(ctx, rpcRequest(cfg, &v1.ShutdownRequest{Force: *force}))
		if err != nil {
			return err
		}
		value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	return nil
}
func printConfigKeys(out io.Writer, prefix string, value any) error {
	if object, ok := value.(map[string]any); ok {
		for key, entry := range object {
			name := key
			if prefix != "" {
				name = prefix + "." + key
			}
			if err := printConfigKeys(out, name, entry); err != nil {
				return err
			}
		}
		return nil
	}
	_, err := fmt.Fprintln(out, prefix)
	return err
}
func rpcNavigation(ctx context.Context, command string, args []string, cfg Config, jsonOut bool) error {
	action := "enter"
	if command == "terminal" {
		if len(args) == 0 {
			return usageErr{errors.New("terminal requires status, ensure, enter or close")}
		}
		action, args = args[0], args[1:]
	}
	fs := newFlagSet(command, cfg.Stderr)
	selected := fs.String("item", "", "item")
	force := fs.Bool("force", false, "force stop or capacity override")
	take := fs.Bool("take", false, "take control in the TUI")
	currentFlag := fs.Bool("current", false, "select current item")
	noAgent := fs.Bool("no-agent", false, "skip fresh observations in picker")
	noPreview := fs.Bool("no-preview", false, "hide preview")
	excludeCurrent := fs.Bool("exclude-current", false, "with pick, leave out the current item")
	var next nextOptions
	if command == "next" {
		fs.BoolVar(&next.deferCurrent, "defer", false, "defer the current item before navigating")
		fs.BoolVar(&next.waitCurrent, "wait", false, "make the current item wait before navigating")
		fs.StringVar(&next.until, "until", "", "with --wait: a duration or local date/time")
		fs.StringVar(&next.on, "on", "", "with --wait: the item to wait on, or - to pick it")
		fs.StringVar(&next.event, "for", "", "with --wait: the external event to wait for")
		fs.BoolVar(&next.closeCurrent, "close", false, "switch to the next item, then close the previous one")
		fs.StringVar(&next.resolution, "resolution", "", "with --close: completed (default), abandoned or superseded")
	}
	var labels repeatFlag
	fs.Var(&labels, "label", "label filter")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	selector, err := itemSelectorFromFlag(fs, *selected)
	if err != nil {
		return usageErr{err}
	}
	if command == "next" {
		if selector != "" {
			return usageErr{errors.New("next accepts label filters, not an item selector")}
		}
		next.labels, next.noAgent = labels, *noAgent
		return rpcNext(ctx, cfg, next, jsonOut)
	}
	if command == "pick" || (command == "switch" && selector == "" && !*currentFlag) {
		if command == "switch" && jsonOut {
			return usageErr{errors.New("interactive wi switch requires a terminal; pass --current or an item selector with --json")}
		}
		candidate, ok, err := rpcPickItem(ctx, cfg, labels, *noAgent, *noPreview, command == "pick" && *excludeCurrent)
		if err != nil {
			return err
		}
		if !ok {
			if command == "pick" {
				return errors.New("no item selected")
			}
			// Cancelling the picker is not an error, so a popup closes.
			return nil
		}
		selector = candidate.Item.ID
	}
	id, err := rpcSelect(ctx, cfg, selector)
	if err != nil {
		return err
	}
	if command == "pick" {
		if jsonOut {
			value, err := cfg.API.Items.GetItem(ctx, rpcRequest(cfg, &v1.GetItemRequest{ItemId: id}))
			if err != nil {
				return err
			}
			return rpcEmit(cfg, true, value.Msg)
		}
		fmt.Fprintln(cfg.Stdout, id)
		return nil
	}
	if command == "terminal" && action == "close" {
		value, err := cfg.API.Agents.StopAgent(ctx, rpcRequest(cfg, &v1.StopAgentRequest{ItemId: id, Force: *force, ConfirmAttachedTerminal: true}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	if action == "status" {
		value, err := cfg.API.Agents.GetAgent(ctx, rpcRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, value.Msg)
	}
	view, err := cfg.API.Agents.GetAgent(ctx, terminalRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
	if err != nil {
		return err
	}
	if !view.Msg.Agent.GetOnline() {
		started, err := cfg.API.Lifecycle.StartItem(ctx, rpcRequest(cfg, &v1.StartItemRequest{ItemId: id, Start: &v1.StartOptions{Mode: v1.AgentMode_AGENT_MODE_TUI, SendNothing: true, OverrideCapacity: *force}}))
		if err != nil {
			return err
		}
		if _, err := rpcWaitJob(ctx, cfg, started.Msg.Job, jsonOut); err != nil {
			return err
		}
	}
	if action == "ensure" {
		fresh, err := cfg.API.Agents.GetAgent(ctx, terminalRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, fresh.Msg)
	}
	return rpcEnterTerminal(ctx, cfg, id, *take, jsonOut)
}
