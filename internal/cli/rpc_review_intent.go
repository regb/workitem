package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
)

func rpcReviewIntent(ctx context.Context, command string, args []string, cfg Config, jsonOut bool) error {
	fs := flag.NewFlagSet("review "+command, flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	item := fs.String("item", "", "work item")
	operation := fs.String("operation-id", "", "operation identity")
	message := fs.String("message", "", "message")
	fs.StringVar(message, "m", "", "message")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("one revision or round identity required")
	}
	id, err := rpcSelect(ctx, cfg, *item)
	if err != nil {
		return err
	}
	selection := fs.Arg(0)
	if command == "intent" {
		out, err := cfg.API.Reviews.GetReviewIntent(ctx, rpcRequest(cfg, &v1.GetReviewIntentRequest{ItemId: id, RevisionId: selection}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, out.Msg)
	}
	if *operation == "" {
		*operation, err = model.NewID()
		if err != nil {
			return err
		}
	}
	if command == "notify" {
		out, err := cfg.API.Reviews.NotifyReviewRound(ctx, rpcRequest(cfg, &v1.NotifyReviewRoundRequest{ItemId: id, RoundId: selection, ClientMessageId: *operation}))
		if err != nil {
			return err
		}
		return rpcEmit(cfg, jsonOut, out.Msg)
	}
	kinds := map[string]string{"approve": "approve", "request-review": "request_review", "request-changes": "request_changes", "withdraw-approval": "withdraw_approval"}
	kind := kinds[command]
	if kind == "" {
		return fmt.Errorf("unknown review intent %q", command)
	}
	out, err := cfg.API.Reviews.SetReviewIntent(ctx, rpcRequest(cfg, &v1.SetReviewIntentRequest{ItemId: id, OperationId: *operation, RevisionId: selection, Kind: kind, Message: *message}))
	if err != nil {
		return err
	}
	return rpcEmit(cfg, jsonOut, out.Msg)
}
