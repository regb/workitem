package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/regb/workitem/internal/app"
)

func runTerminal(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("usage: wi terminal <status|ensure|enter|close> [options] [item]")}
	}
	switch args[0] {
	case "status":
		return runTerminalStatus(ctx, args[1:], cfg, jsonOut)
	case "ensure":
		return runTerminalEnsure(ctx, args[1:], cfg, jsonOut)
	case "enter":
		return runTerminalEnter(ctx, args[1:], cfg, jsonOut)
	case "close":
		return runTerminalClose(ctx, args[1:], cfg, jsonOut)
	default:
		return usageErr{fmt.Errorf("unknown terminal command %q", args[0])}
	}
}

func runTerminalStatus(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	selector, err := workspaceSelector("terminal status", args, cfg)
	if err != nil {
		return err
	}
	res, err := cfg.App.TerminalStatus(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "work item: %s\nsession: %s\nexists: %t\ncurrent: %t\n", res.WorkItemID, res.Session, res.Exists, res.Current)
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}

func runTerminalEnsure(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	selector, err := workspaceSelector("terminal ensure", args, cfg)
	if err != nil {
		return err
	}
	res, err := cfg.App.EnsureTerminal(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	return printTerminalResult(cfg, res, jsonOut, "ensured")
}

func runTerminalEnter(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	selector, err := workspaceSelector("terminal enter", args, cfg)
	if err != nil {
		return err
	}
	res, err := cfg.App.EnterTerminal(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}, !jsonOut)
	if err != nil {
		return err
	}
	return printTerminalResult(cfg, res, jsonOut, "entered")
}

func runTerminalClose(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	selector, err := workspaceSelector("terminal close", args, cfg)
	if err != nil {
		return err
	}
	res, err := cfg.App.CloseTerminal(ctx, app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	if res.Changed {
		fmt.Fprintf(cfg.Stdout, "closed terminal for %s\n", res.WorkItemID)
	} else {
		fmt.Fprintf(cfg.Stdout, "terminal already closed for %s\n", res.WorkItemID)
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}

func printTerminalResult(cfg Config, res app.TerminalResult, jsonOut bool, verb string) error {
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "%s terminal for %s\n", verb, res.WorkItemID)
	fmt.Fprintf(cfg.Stdout, "session: %s\n", res.Session)
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}
