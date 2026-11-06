package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func interactiveCLI(cfg Config) bool {
	if cfg.Interactive != nil {
		return cfg.Interactive()
	}
	// Popup invocations can redirect output but still name the originating client.
	if cfg.Env["WI_TMUX_CLIENT"] != "" {
		return true
	}
	in, ok := cfg.Stdin.(*os.File)
	if !ok {
		return false
	}
	out, ok := cfg.Stdout.(*os.File)
	return ok && terminalFile(in) && terminalFile(out)
}
func terminalRequest[T any](cfg Config, value *T) *connect.Request[T] {
	req := rpcRequest(cfg, value)
	req.Header().Set("Wi-Client-Kind", "terminal")
	return req
}
func rpcEnterTerminal(ctx context.Context, cfg Config, id string, take bool, jsonOut bool) error {
	view, err := cfg.API.Agents.GetAgent(ctx, terminalRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
	if err != nil {
		return err
	}
	if view.Msg.Agent == nil {
		return errors.New("agent status is unavailable")
	}
	// Enter the TUI as a terminal controller, not as a structured web controller.
	control, err := cfg.API.Control.GetControl(ctx, terminalRequest(cfg, &v1.GetControlRequest{ItemId: id, ClientId: rpcClientID(cfg)}))
	if err != nil {
		return err
	}
	plan := control.Msg.Control.GetPlan()
	if plan.GetNeedsConfirmation() && !take {
		if view.Msg.Agent.Terminal == nil {
			return errors.New("another client controls this agent without a terminal; use wi switch --take")
		}
		fmt.Fprintln(cfg.Stderr, "read-only: another client controls this agent; use wi switch --take")
	} else {
		request := terminalRequest(cfg, &v1.AcquireControlRequest{ItemId: id, ClientId: rpcClientID(cfg), StartAgent: true, Confirm: take, PlanToken: plan.GetPlanToken()})
		acquired, err := cfg.API.Control.AcquireControl(ctx, request)
		if err != nil {
			return err
		}
		if _, err = rpcWaitJob(ctx, cfg, acquired.Msg.Job, false); err != nil {
			return err
		}
		// The acquisition may replace a headless runtime with a TUI.
		view, err = cfg.API.Agents.GetAgent(ctx, terminalRequest(cfg, &v1.GetAgentRequest{ItemId: id}))
		if err != nil {
			return err
		}
	}
	target := view.Msg.Agent.GetTerminal()
	if target == nil {
		return errors.New("agent has no terminal")
	}
	if _, err := cfg.API.Items.MarkRead(ctx, rpcRequest(cfg, &v1.MarkReadRequest{ItemId: id})); err != nil {
		return err
	}
	current, err := rpcSelect(ctx, cfg, "")
	if err == nil && current != id {
		previous, e := cfg.API.Items.GetItem(ctx, rpcRequest(cfg, &v1.GetItemRequest{ItemId: current}))
		if e == nil {
			activity := previous.Msg.Observation.GetActivity()
			explicitUnread := activity.GetLastUnreadAt() != nil && (activity.GetLastReadAt() == nil || activity.LastUnreadAt.AsTime().After(activity.LastReadAt.AsTime()))
			if !explicitUnread {
				if _, e = cfg.API.Items.MarkRead(ctx, rpcRequest(cfg, &v1.MarkReadRequest{ItemId: current})); e != nil {
					fmt.Fprintln(cfg.Stderr, "warning: could not mark previous item read:", e)
				}
			}
		}
	}
	if jsonOut {
		return rpcEmit(cfg, true, view.Msg)
	}
	if !interactiveCLI(cfg) {
		return renderRPC(cfg.Stdout, view.Msg)
	}
	return runTerminalTarget(ctx, cfg, target)
}
func runTerminalTarget(ctx context.Context, cfg Config, target *v1.Terminal) error {
	client := strings.TrimSpace(cfg.Env["WI_TMUX_CLIENT"])
	if strings.Contains(client, "#{") {
		return errors.New("WI_TMUX_CLIENT must be an expanded tmux client name")
	}
	argv := []string{}
	if target.TmuxSocket != "" {
		argv = append(argv, "-S", target.TmuxSocket)
	}
	destination := target.Session
	if target.Window != "" {
		destination += ":" + target.Window
	}
	if cfg.Env["TMUX"] != "" || client != "" {
		argv = append(argv, "switch-client")
		if client != "" {
			argv = append(argv, "-c", client)
		}
		argv = append(argv, "-t", destination)
	} else {
		argv = append(argv, "attach-session", "-t", destination)
	}
	if cfg.RunTerminal != nil {
		return cfg.RunTerminal(ctx, argv)
	}
	command := exec.CommandContext(ctx, "tmux", argv...)
	command.Stdin = cfg.Stdin
	command.Stdout = cfg.Stdout
	command.Stderr = cfg.Stderr
	return command.Run()
}
