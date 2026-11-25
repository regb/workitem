package cli

import (
	"context"
	"errors"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func rpcMerge(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("merge", cfg.Stderr)
	selected := fs.String("item", "", "source item")
	target := fs.String("target", "", "target branch")
	local := fs.Bool("local", false, "skip upstream synchronization")
	rebase := fs.Bool("rebase", false, "preserve rebased commit series")
	message := fs.String("message", "", "squash message, defaults to item title")
	fs.StringVar(message, "m", "", "squash message")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("merge takes at most one target")
	}
	if *rebase && *message != "" {
		return errors.New("--message is only used by squash merge")
	}
	selector := *selected
	if fs.NArg() == 1 {
		if *target != "" {
			return errors.New("pass a positional target or --target, not both")
		}
		*target = fs.Arg(0)
	}
	id, err := rpcSelect(ctx, cfg, selector)
	if err != nil {
		return err
	}
	request := &v1.IntegrateItemRequest{ItemId: id, TargetBranch: *target, Local: *local, Rebase: *rebase, Message: *message}
	// An agent merging its own item does so from inside its turn; its
	// credential tells the daemon, and the merge does not wait for that turn.
	value, err := cfg.API.Repositories.IntegrateItem(ctx, rpcRequest(cfg, request))
	if err != nil {
		return err
	}
	value.Msg.Job, err = rpcWaitJob(ctx, cfg, value.Msg.Job, jsonOut)
	if err != nil {
		return err
	}
	return rpcEmit(cfg, jsonOut, value.Msg)
}
