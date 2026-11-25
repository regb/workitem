package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apiview"
	"github.com/regb/workitem/internal/model"
)

// rpcPropose asks the item's user to file an item, from the current item.
// It returns at once: nothing exists until the user files it.
func rpcPropose(ctx context.Context, cfg Config, from, title, descriptionFile string, labels []string, jsonOut bool) error {
	if strings.TrimSpace(title) == "" {
		return usageErr{errors.New("--propose requires --title")}
	}
	id, err := rpcSelect(ctx, cfg, from)
	if err != nil {
		return err
	}
	description, err := readDescriptionFileFlag(cfg, descriptionFile)
	if err != nil {
		return err
	}
	normalized, err := model.NormalizeLabels(labels)
	if err != nil {
		return usageErr{err}
	}
	value, err := cfg.API.Items.ProposeItem(ctx, rpcRequest(cfg, &v1.ProposeItemRequest{ItemId: id, Title: title, Description: description, Labels: normalized, RequestId: newCommandID()}))
	if err != nil {
		return err
	}
	if jsonOut {
		return rpcEmit(cfg, true, value.Msg)
	}
	fmt.Fprintf(cfg.Stdout, "proposed %s %q: waiting for the user to file it\n", value.Msg.Proposal.Id, value.Msg.Proposal.Title)
	return nil
}

// rpcProposal lists, files and dismisses proposals.
func rpcProposal(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	action := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	fs := newFlagSet("proposal "+action, cfg.Stderr)
	switch action {
	case "list":
		item := fs.String("item", "", "item")
		all := fs.Bool("all", false, "include filed and dismissed")
		if err := rpcParse(fs, args); err != nil {
			return err
		}
		request := &v1.ListProposalsRequest{PendingOnly: !*all}
		if *item != "" {
			id, err := rpcSelect(ctx, cfg, *item)
			if err != nil {
				return err
			}
			request.ItemId = id
		}
		value, err := cfg.API.Items.ListProposals(ctx, rpcRequest(cfg, request))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		for _, p := range value.Msg.Proposals {
			state := strings.ToLower(strings.TrimPrefix(p.State.String(), "PROPOSAL_STATE_"))
			fmt.Fprintf(cfg.Stdout, "%s\t%s\t%s\tfrom %s\t%s\n", p.Id, state, apiview.Time(p.CreatedAt).Format("2006-01-02 15:04"), model.ShortID(p.ItemId), p.Title)
		}
		return nil
	case "file":
		title := fs.String("title", "", "title")
		description := fs.String("desc-file", "", "description file")
		var labels repeatFlag
		fs.Var(&labels, "label", "label")
		if err := rpcParse(fs, args); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return usageErr{errors.New("usage: wi proposal file ID [--title TEXT] [--desc-file PATH] [--label LABEL]")}
		}
		content, err := readDescriptionFileFlag(cfg, *description)
		if err != nil {
			return err
		}
		value, err := cfg.API.Items.FileProposal(ctx, rpcRequest(cfg, &v1.FileProposalRequest{ProposalId: fs.Arg(0), Title: *title, Description: content, ReplaceLabels: len(labels) > 0, Labels: labels}))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		fmt.Fprintf(cfg.Stdout, "filed %s %s\n", value.Msg.Item.Id, value.Msg.Item.Title)
		return nil
	case "dismiss":
		reason := fs.String("reason", "", "why")
		if err := rpcParse(fs, args); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return usageErr{errors.New("usage: wi proposal dismiss ID [--reason TEXT]")}
		}
		value, err := cfg.API.Items.DismissProposal(ctx, rpcRequest(cfg, &v1.DismissProposalRequest{ProposalId: fs.Arg(0), Reason: *reason}))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		fmt.Fprintf(cfg.Stdout, "dismissed %s\n", value.Msg.Proposal.Id)
		return nil
	}
	return usageErr{fmt.Errorf("unknown proposal action %q", action)}
}
