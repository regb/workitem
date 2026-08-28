package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/regb/workitem/internal/agent"
	"github.com/regb/workitem/internal/app"
)

func runAgentControl(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("usage: wi agent control <send|abort|shutdown> [options]")}
	}
	switch args[0] {
	case "send":
		return runAgentControlMessage(ctx, args[1:], cfg, jsonOut)
	case agent.CommandAbort, agent.CommandShutdown:
		return runAgentControlSignal(ctx, args[0], args[1:], cfg, jsonOut)
	default:
		return usageErr{fmt.Errorf("unknown agent control command %q", args[0])}
	}
}

func runAgentControlMessage(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("agent control send", cfg.Stderr)
	var item, actor, filePath string
	var fromStdin, followUp bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.StringVar(&actor, "actor", "", "control actor/source")
	fs.StringVar(&filePath, "file", "", "read message from file")
	fs.BoolVar(&fromStdin, "stdin", false, "read message from stdin")
	fs.BoolVar(&followUp, "follow-up", false, "deliver after the current agent run settles")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	message, err := readAgentControlMessage(cfg, fs.Args(), filePath, fromStdin)
	if err != nil {
		return usageErr{err}
	}
	commandType := agent.CommandSteer
	if followUp {
		commandType = agent.CommandFollowUp
	}
	res, err := cfg.App.AgentControl(ctx, app.AgentControlOptions{
		ResolveOptions: app.ResolveOptions{Selector: item, CWD: cfg.CWD, Env: cfg.Env},
		CommandType:    commandType, Message: message, Actor: actor,
	})
	if err != nil {
		return err
	}
	return printAgentControlResult(cfg, res, jsonOut)
}

func runAgentControlSignal(ctx context.Context, commandType string, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("agent control "+commandType, cfg.Stderr)
	var item, actor string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.StringVar(&actor, "actor", "", "control actor/source")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if len(fs.Args()) != 0 {
		return usageErr{fmt.Errorf("agent control %s does not accept positional arguments", commandType)}
	}
	res, err := cfg.App.AgentControl(ctx, app.AgentControlOptions{
		ResolveOptions: app.ResolveOptions{Selector: item, CWD: cfg.CWD, Env: cfg.Env}, CommandType: commandType, Actor: actor,
	})
	if err != nil {
		return err
	}
	return printAgentControlResult(cfg, res, jsonOut)
}

func printAgentControlResult(cfg Config, res app.AgentControlResult, jsonOut bool) error {
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "submitted %s control command %s to runtime %s\n", res.Command.Type, res.Command.ID, res.Runtime.ID)
	return nil
}
