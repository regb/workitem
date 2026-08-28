package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/regb/workitem/internal/app"
)

func runDelete(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("delete", cfg.Stderr)
	var item string
	var archived, yes bool
	fs.StringVar(&item, "item", "", "work item selector")
	fs.BoolVar(&archived, "archived", false, "delete every safely cleaned archived work item")
	fs.BoolVar(&yes, "yes", false, "confirm irreversible deletion")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if !yes {
		return usageErr{errors.New("delete is irreversible; pass --yes to confirm")}
	}
	if archived {
		if item != "" || len(fs.Args()) != 0 {
			return usageErr{errors.New("pass either --archived or one item, not both")}
		}
	} else {
		selector, err := itemSelectorFromFlag(fs, item)
		if err != nil {
			return usageErr{err}
		}
		item = selector
	}
	res, err := cfg.App.DeleteWorkItems(ctx, app.DeleteWorkItemsOptions{ResolveOptions: app.ResolveOptions{Selector: item, CWD: cfg.CWD, Env: cfg.Env}, Archived: archived})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	if res.Count == 0 {
		fmt.Fprintln(cfg.Stdout, "no archived work items to delete")
	} else {
		fmt.Fprintf(cfg.Stdout, "deleted %d archived work item(s)\n", res.Count)
		for _, id := range res.DeletedIDs {
			fmt.Fprintf(cfg.Stdout, "  %s\n", id)
		}
	}
	for _, warning := range res.Warnings {
		fmt.Fprintf(cfg.Stdout, "warning: %s\n", warning)
	}
	return nil
}
