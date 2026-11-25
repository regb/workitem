package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/model"
)

// rpcAccess lists, grants and revokes what items' agents may do to other
// items.
func rpcAccess(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	action := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	fs := newFlagSet("access "+action, cfg.Stderr)
	switch action {
	case "list":
		if err := rpcParse(fs, args); err != nil {
			return err
		}
		value, err := cfg.API.Items.ListAccess(ctx, rpcRequest(cfg, &v1.ListAccessRequest{}))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		fmt.Fprintln(cfg.Stdout, "An item's agent may always manage its own item and the items it delegated.")
		if len(value.Msg.Entries) == 0 {
			fmt.Fprintln(cfg.Stdout, "No other access: agents may not act on other items.")
		}
		for _, e := range value.Msg.Entries {
			fmt.Fprintf(cfg.Stdout, "%s\t%s on %s\t%s\n", e.Id, subjectText(e.Subject), onText(e.ItemId), levelName(e.Level))
		}
		return nil
	case "grant":
		item := fs.String("item", "", "the item whose agent is granted")
		every := fs.Bool("every-item", false, "grant every item's agent")
		on := fs.String("on", "", "the item granted on")
		everything := fs.Bool("everything", false, "grant on every item")
		level := fs.String("level", "", "view, message, control, integrate or manage")
		if err := rpcParse(fs, args); err != nil {
			return err
		}
		if (*item == "") == !*every || (*on == "") == !*everything || *level == "" || fs.NArg() != 0 {
			return usageErr{errors.New("usage: wi access grant (--item ITEM | --every-item) (--on ITEM | --everything) --level LEVEL")}
		}
		parsed, err := model.ParseAccessLevel(*level)
		if err != nil {
			return usageErr{err}
		}
		request := &v1.GrantAccessRequest{Subject: model.EveryItemPrincipal, OnItemId: model.EveryItem, Level: v1.AccessLevel(parsed)}
		if *item != "" {
			id, err := rpcSelect(ctx, cfg, *item)
			if err != nil {
				return err
			}
			request.Subject = model.ItemPrincipal(id)
		}
		if *on != "" {
			id, err := rpcSelect(ctx, cfg, *on)
			if err != nil {
				return err
			}
			request.OnItemId = id
		}
		value, err := cfg.API.Items.GrantAccess(ctx, rpcRequest(cfg, request))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		fmt.Fprintf(cfg.Stdout, "%s may %s %s\n", subjectText(value.Msg.Entry.Subject), levelName(value.Msg.Entry.Level), onText(value.Msg.Entry.ItemId))
		return nil
	case "revoke":
		if err := rpcParse(fs, args); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return usageErr{errors.New("usage: wi access revoke ENTRY-ID")}
		}
		value, err := cfg.API.Items.RevokeAccess(ctx, rpcRequest(cfg, &v1.RevokeAccessRequest{EntryId: fs.Arg(0)}))
		if err != nil {
			return err
		}
		if jsonOut {
			return rpcEmit(cfg, true, value.Msg)
		}
		fmt.Fprintln(cfg.Stdout, "revoked", fs.Arg(0))
		return nil
	}
	return usageErr{fmt.Errorf("unknown access action %q", action)}
}

func subjectText(subject string) string {
	if subject == model.EveryItemPrincipal {
		return "every item's agent"
	}
	return subject + "'s agent"
}

func onText(itemID string) string {
	if itemID == model.EveryItem {
		return "every item"
	}
	return "item " + itemID
}

func levelName(level v1.AccessLevel) string {
	return model.AccessLevel(level).String()
}
