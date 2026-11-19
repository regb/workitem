package cli

import (
	"context"
	"errors"
	"flag"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

func rpcReviewCaptureStatus(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := flag.NewFlagSet("review capture-status", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	item := fs.String("item", "", "work item")
	if err := rpcParse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("capture-status takes no positional arguments")
	}
	id, err := rpcSelect(ctx, cfg, *item)
	if err != nil {
		return err
	}
	v, err := cfg.API.Items.GetItem(ctx, rpcRequest(cfg, &v1.GetItemRequest{ItemId: id}))
	if err != nil {
		return err
	}
	return rpcEmit(cfg, jsonOut, v.Msg.Observation)
}
