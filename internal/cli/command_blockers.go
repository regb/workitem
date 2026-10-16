package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

// waitConditions are the flags shared by wi wait and wi next --wait. Each
// flag adds exactly one blocker, so combining them waits for all of them.
type waitConditions struct {
	until, on, waitFor string
}

func (c *waitConditions) register(fs *flag.FlagSet) {
	fs.StringVar(&c.until, "until", "", "wait until a time: a duration such as 90m, 4h, 2d, or a local date/time such as 2026-10-20 or 2026-10-20T09:00")
	fs.StringVar(&c.on, "on", "", "wait until another work item closes; - picks it interactively")
	fs.StringVar(&c.waitFor, "for", "", "wait for an external event, named by the text; cleared with `wi wait --clear` or by automation")
}

func (c waitConditions) empty() bool {
	return strings.TrimSpace(c.until) == "" && strings.TrimSpace(c.on) == "" && strings.TrimSpace(c.waitFor) == ""
}

func (c waitConditions) blockers(ctx context.Context, cfg Config) ([]model.Blocker, error) {
	until, on, waitFor := strings.TrimSpace(c.until), strings.TrimSpace(c.on), strings.TrimSpace(c.waitFor)
	blockers := []model.Blocker{}
	if until != "" {
		at, err := parseBlockUntil(time.Now(), until)
		if err != nil {
			return nil, usageErr{err}
		}
		blockers = append(blockers, model.Blocker{Kind: model.BlockerTime, Until: &at})
	}
	if on == "-" {
		// "-" chooses the other item in the interactive picker.
		selected, ok, err := pickItem(ctx, cfg, nil, false, false, true)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("no item selected")
		}
		on = selected.Item.ID
	}
	if on != "" {
		target, err := cfg.App.ResolveItem(ctx, appResolve(cfg, on))
		if err != nil {
			return nil, fmt.Errorf("resolve --on item: %w", err)
		}
		blockers = append(blockers, model.Blocker{Kind: model.BlockerItem, ItemID: target.ID})
	}
	if waitFor != "" {
		blockers = append(blockers, model.Blocker{Kind: model.BlockerExternal, Event: waitFor})
	}
	if len(blockers) == 0 {
		return nil, usageErr{errors.New("wait requires --until, --on, or --for")}
	}
	return blockers, nil
}

func runWait(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("wait", cfg.Stderr)
	var item string
	var clear bool
	var conditions waitConditions
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.BoolVar(&clear, "clear", false, "stop waiting: clear every blocker")
	conditions.register(fs)
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	if clear {
		if !conditions.empty() {
			return usageErr{errors.New("--clear cannot be combined with wait conditions")}
		}
		res, err := coordinatorClearBlockers(ctx, cfg, selector, "")
		if err != nil {
			return err
		}
		if jsonOut {
			return writeJSON(cfg.Stdout, res)
		}
		printBlockerResult(cfg, res)
		return nil
	}
	blockers, err := conditions.blockers(ctx, cfg)
	if err != nil {
		return err
	}
	res, err := coordinatorBlockerCommand(ctx, cfg, selector, blockers)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	printBlockerResult(cfg, res)
	return nil
}

func runBlocker(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	if len(args) == 0 {
		return usageErr{errors.New("usage: wi blocker <add|list|clear> [options]")}
	}
	switch args[0] {
	case "add":
		return runBlockerAdd(ctx, args[1:], cfg, jsonOut)
	case "list":
		return runBlockerList(ctx, args[1:], cfg, jsonOut)
	case "clear":
		return runBlockerClear(ctx, args[1:], cfg, jsonOut)
	default:
		return usageErr{fmt.Errorf("unknown blocker command %q", args[0])}
	}
}

func runBlockerAdd(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("blocker add", cfg.Stderr)
	var item, kind, until, on, event, label string
	var meta repeatFlag
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.StringVar(&kind, "kind", "", "blocker kind: time, item, or external")
	fs.StringVar(&until, "until", "", "time kind: a duration or local date/time")
	fs.StringVar(&on, "on", "", "item kind: the work item that must close first")
	fs.StringVar(&event, "event", "", "external kind: the event it waits for")
	fs.StringVar(&label, "label", "", "short display form for lists, such as #1234 (up to 40 characters)")
	fs.Var(&meta, "meta", "structured metadata key=value; repeatable")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	parsedMeta, err := model.ParseBlockerMeta(meta)
	if err != nil {
		return usageErr{err}
	}
	blocker := model.Blocker{Kind: strings.TrimSpace(kind), Label: strings.TrimSpace(label), Meta: parsedMeta}
	switch blocker.Kind {
	case model.BlockerTime:
		if strings.TrimSpace(until) == "" || on != "" || event != "" {
			return usageErr{errors.New("--kind time requires --until and excludes --on and --event")}
		}
		at, err := parseBlockUntil(time.Now(), until)
		if err != nil {
			return usageErr{err}
		}
		blocker.Until = &at
	case model.BlockerItem:
		if strings.TrimSpace(on) == "" || until != "" || event != "" {
			return usageErr{errors.New("--kind item requires --on and excludes --until and --event")}
		}
		target, err := cfg.App.ResolveItem(ctx, appResolve(cfg, on))
		if err != nil {
			return fmt.Errorf("resolve --on item: %w", err)
		}
		blocker.ItemID = target.ID
	case model.BlockerExternal:
		if strings.TrimSpace(event) == "" || until != "" || on != "" {
			return usageErr{errors.New("--kind external requires --event and excludes --until and --on")}
		}
		blocker.Event = strings.TrimSpace(event)
	default:
		return usageErr{errors.New("usage: wi blocker add --kind <time|item|external> (--until WHEN | --on ITEM | --event NAME) [--label TEXT] [--meta key=value]... [item]")}
	}
	res, err := coordinatorBlockerCommand(ctx, cfg, selector, []model.Blocker{blocker})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	printBlockerResult(cfg, res)
	return nil
}

type blockerListResult struct {
	WorkItemID string          `json:"work_item_id"`
	Waiting    bool            `json:"waiting"`
	Blockers   []model.Blocker `json:"blockers"`
}

func runBlockerList(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("blocker list", cfg.Stderr)
	var item string
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	manifest, err := cfg.App.ResolveItem(ctx, appResolve(cfg, selector))
	if err != nil {
		return err
	}
	res := blockerListResult{WorkItemID: manifest.ID, Waiting: manifest.Waiting(time.Now()), Blockers: append([]model.Blocker{}, manifest.Blockers...)}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	if len(res.Blockers) == 0 {
		fmt.Fprintf(cfg.Stdout, "%s: no blockers\n", res.WorkItemID)
		return nil
	}
	for _, blocker := range res.Blockers {
		line := fmt.Sprintf("%s  %-8s  %s", blocker.ID, blocker.Kind, blocker.Describe())
		if blocker.Label != "" {
			line += "  (" + blocker.Label + ")"
		}
		if pairs := blocker.MetaPairs(); len(pairs) > 0 {
			line += "  [" + strings.Join(pairs, " ") + "]"
		}
		fmt.Fprintln(cfg.Stdout, line)
	}
	return nil
}

func runBlockerClear(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("blocker clear", cfg.Stderr)
	var item, id string
	var all bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.StringVar(&id, "id", "", "the blocker to clear")
	fs.BoolVar(&all, "all", false, "clear every blocker")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	id = strings.TrimSpace(id)
	if all == (id != "") {
		return usageErr{errors.New("usage: wi blocker clear (--id ID | --all) [--item <selector>] [item]")}
	}
	res, err := coordinatorClearBlockers(ctx, cfg, selector, id)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	printBlockerResult(cfg, res)
	return nil
}

func printBlockerResult(cfg Config, res app.BlockerResult) {
	for _, blocker := range res.Added {
		fmt.Fprintf(cfg.Stdout, "%s: waiting %s (blocker %s)\n", res.WorkItemID, blocker.Describe(), blocker.ID)
	}
	for _, blocker := range res.Cleared {
		fmt.Fprintf(cfg.Stdout, "%s: cleared blocker %s (%s)\n", res.WorkItemID, blocker.ID, blocker.Describe())
	}
	if !res.Changed {
		fmt.Fprintf(cfg.Stdout, "%s: unchanged\n", res.WorkItemID)
	}
	if !res.Waiting && len(res.Cleared) > 0 {
		fmt.Fprintf(cfg.Stdout, "%s: no longer waiting\n", res.WorkItemID)
	}
}

func runClose(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("close", cfg.Stderr)
	var item, resolution string
	var force bool
	fs.StringVar(&item, "item", "", "work item selector (default: current item)")
	fs.StringVar(&resolution, "resolution", model.ResolutionCompleted, "how the work ended: completed, abandoned, or superseded")
	fs.BoolVar(&force, "force", false, "allow closing while retaining an unclosable current terminal")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	selector, err := itemSelectorFromFlag(fs, item)
	if err != nil {
		return usageErr{err}
	}
	resolution = strings.TrimSpace(resolution)
	if !model.ValidResolution(resolution) {
		return usageErr{fmt.Errorf("invalid resolution %q; expected completed, abandoned, or superseded", resolution)}
	}
	res, err := cfg.App.Close(ctx, appResolve(cfg, selector), resolution, force)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	printTransition(cfg.Stdout, res)
	if res.Changed {
		fmt.Fprintf(cfg.Stdout, "resolution: %s\n", resolution)
	}
	return nil
}

// snoozeHour is when named days (tomorrow, monday, ...) begin.
const snoozeHour = 9

// parseBlockUntil accepts a relative duration (Go syntax plus a d suffix for
// days), "tomorrow" or a weekday name meaning the next one at 09:00 local, or
// an absolute local date or date-time.
func parseBlockUntil(now time.Time, value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if at, ok := namedDay(now, strings.ToLower(value)); ok {
		return at, nil
	}
	if strings.HasSuffix(value, "d") {
		if days, err := strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64); err == nil && days > 0 {
			return now.Add(time.Duration(days * float64(24*time.Hour))), nil
		}
	}
	if duration, err := time.ParseDuration(value); err == nil {
		if duration <= 0 {
			return time.Time{}, fmt.Errorf("--until duration must be positive")
		}
		return now.Add(duration), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if at, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			if !at.After(now) {
				return time.Time{}, fmt.Errorf("--until %s is not in the future", value)
			}
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid --until %q; use a duration such as 4h or 2d, tomorrow or a weekday, or a date such as 2026-10-20", value)
}

// namedDay resolves tomorrow or a weekday to the next such day at
// snoozeHour, strictly after now.
func namedDay(now time.Time, value string) (time.Time, bool) {
	local := now.In(time.Local)
	morning := func(days int) time.Time {
		day := local.AddDate(0, 0, days)
		return time.Date(day.Year(), day.Month(), day.Day(), snoozeHour, 0, 0, 0, time.Local)
	}
	if value == "tomorrow" {
		return morning(1), true
	}
	for weekday := time.Sunday; weekday <= time.Saturday; weekday++ {
		if value != strings.ToLower(weekday.String()) {
			continue
		}
		days := (int(weekday) - int(local.Weekday()) + 7) % 7
		if at := morning(days); days > 0 || at.After(now) {
			return at, true
		}
		return morning(7), true
	}
	return time.Time{}, false
}

func appResolve(cfg Config, selector string) app.ResolveOptions {
	return app.ResolveOptions{Selector: selector, CWD: cfg.CWD, Env: cfg.Env}
}
