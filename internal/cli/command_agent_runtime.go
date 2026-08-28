package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
)

func runAgentRuntime(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("usage: wi agent runtime <status|ensure|stop> [options] [item]")}
	}
	switch args[0] {
	case "status":
		return runAgentRuntimeStatus(ctx, args[1:], cfg, jsonOut)
	case "ensure":
		return runAgentRuntimeEnsure(ctx, args[1:], cfg, jsonOut)
	case "stop":
		return runAgentRuntimeStop(ctx, args[1:], cfg, jsonOut)
	default:
		return usageErr{fmt.Errorf("unknown agent runtime command %q", args[0])}
	}
}

func runAgentRuntimeStatus(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("agent runtime status", cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.AgentRuntimeStatus(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "work item: %s\n", res.WorkItemID)
	if res.Runtime == nil {
		fmt.Fprintln(cfg.Stdout, "agent runtime: absent")
		return nil
	}
	fmt.Fprintf(cfg.Stdout, "runtime: %s\n", res.Runtime.ID)
	fmt.Fprintf(cfg.Stdout, "mode: %s\n", res.Runtime.Mode)
	fmt.Fprintf(cfg.Stdout, "state: %s\n", res.Runtime.State)
	fmt.Fprintf(cfg.Stdout, "online: %t\n", res.Online)
	if res.Runtime.HostPID > 0 {
		fmt.Fprintf(cfg.Stdout, "host pid: %d\n", res.Runtime.HostPID)
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}

func runAgentRuntimeEnsure(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("agent runtime ensure", cfg.Stderr)
	var item, modeValue string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.StringVar(&modeValue, "mode", "", "runtime mode: tui or rpc")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	if strings.TrimSpace(modeValue) == "" {
		return usageErr{errors.New("--mode is required; expected tui or rpc")}
	}
	mode, err := agent.ParseMode(modeValue)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.EnsureAgentRuntime(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}, mode)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	verb := "using"
	if res.Created {
		verb = "started"
	}
	fmt.Fprintf(cfg.Stdout, "%s %s agent runtime %s for %s\n", verb, res.Runtime.Mode, res.Runtime.ID, res.WorkItemID)
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}

func runAgentRuntimeStop(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("agent runtime stop", cfg.Stderr)
	var item string
	var force bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.BoolVar(&force, "force", false, "abort a busy turn before requesting shutdown")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	res, err := cfg.App.StopAgentRuntime(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}, force)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	if res.Changed {
		fmt.Fprintf(cfg.Stdout, "requested agent runtime shutdown for %s\n", res.WorkItemID)
	} else {
		fmt.Fprintf(cfg.Stdout, "agent runtime already stopped for %s\n", res.WorkItemID)
	}
	return nil
}
