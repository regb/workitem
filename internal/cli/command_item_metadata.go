package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func runLabel(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("label", cfg.Stderr)
	var item string
	var remove bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.BoolVar(&remove, "remove", false, "remove labels instead of adding them")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	labels := fs.Args()
	if len(labels) > 0 {
		switch labels[0] {
		case "add", "remove", "list":
			return usageErr{fmt.Errorf("label subcommands were removed; use `wi label [--item <selector>] <label>...`, `wi label --remove [--item <selector>] <label>...`, or `wi label [--item <selector>]`")}
		}
	}
	if len(labels) == 0 {
		res, err := cfg.App.ListLabels(ctx, app.ResolveOptions{Selector: item, CWD: cfg.CWD, Env: cfg.Env})
		if err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, res)
		}
		fmt.Fprintln(cfg.Stdout, strings.Join(res.Labels, "\n"))
		return nil
	}
	commandType := coordinator.CommandLabelsAdd
	if remove {
		commandType = coordinator.CommandLabelsRemove
	}
	before, commandResult, err := executeCoordinatorManifestCommand(ctx, cfg, item, coordinatorManifestCommandOptions{commandType: commandType, labels: labels})
	res := app.LabelResult{}
	if err == nil {
		changed := []string{}
		for _, label := range labels {
			normalized, normalizeErr := model.NormalizeLabel(label)
			if normalizeErr != nil {
				err = normalizeErr
				break
			}
			was, now := model.HasLabel(before.Manifest.Labels, normalized), model.HasLabel(commandResult.Manifest.Labels, normalized)
			if was != now {
				changed = append(changed, normalized)
			}
		}
		res = app.LabelResult{WorkItemID: commandResult.Manifest.ID, Labels: commandResult.Manifest.Labels, Changed: changed, Manifest: commandResult.Manifest, Warnings: []string{}}
	}
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	fmt.Fprintf(cfg.Stdout, "labels for %s: %s\n", res.WorkItemID, strings.Join(res.Labels, ", "))
	return nil
}

func runDeep(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("deep", cfg.Stderr)
	var item string
	var clear bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.BoolVar(&clear, "clear", false, "clear deep work on the work item")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	deep := !clear
	_, commandResult, err := executeCoordinatorManifestCommand(ctx, cfg, selector, coordinatorManifestCommandOptions{commandType: coordinator.CommandDeepWorkSet, deepWork: &deep})
	res := app.DeepWorkResult{}
	if err == nil {
		capacity := cfg.App.DeepWorkCapacity()
		res = app.DeepWorkResult{WorkItemID: commandResult.Manifest.ID, DeepWork: commandResult.Manifest.DeepWork, Changed: commandResult.Changed, Capacity: &capacity, Manifest: commandResult.Manifest, Warnings: []string{}}
	}
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	state := "enabled"
	if !res.DeepWork {
		state = "disabled"
	}
	if res.Changed {
		fmt.Fprintf(cfg.Stdout, "deep work %s for %s\n", state, res.WorkItemID)
	} else {
		fmt.Fprintf(cfg.Stdout, "deep work already %s for %s\n", state, res.WorkItemID)
	}
	return nil
}
