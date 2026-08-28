package cli

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/regb/workitem/internal/app"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/model"
)

func enrichWorkListAgentStatus(ctx context.Context, cfg Config, res *app.WorkListResult) (map[string]string, []string) {
	warnings := cfg.App.EnrichWorkList(ctx, res, app.ResolveOptions{CWD: cfg.CWD, Env: cfg.Env})
	labels := map[string]string{}
	for _, item := range append(append([]app.WorkListItem{}, res.Sections.Working...), res.Sections.Waiting...) {
		if item.Agent != nil {
			labels[item.ID] = agentDisplayLabel(item.Agent.Label, item.Agent.Marker)
		}
	}
	return labels, warnings
}

func agentListBucket(label string) string {
	switch agentBaseStatus(label) {
	case "problem", "?":
		return "needs_fixing"
	case "busy":
		return "in_progress"
	default:
		return "needs_attention"
	}
}

func agentDisplayLabel(label, marker string) string {
	if strings.TrimSpace(marker) != "" {
		return marker
	}
	return label
}

type workDisplaySection struct {
	Name  string
	Items []app.WorkListItem
}

func runList(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("list", cfg.Stderr)
	var archived, all, noAgent, ids bool
	var labels repeatFlag
	var state string
	fs.BoolVar(&archived, "archived", false, "show archived work items only")
	fs.BoolVar(&all, "all", false, "show active and archived work items")
	fs.BoolVar(&noAgent, "no-agent", false, "do not inspect primary agent/worktree status for visible active work items")
	fs.BoolVar(&ids, "ids", false, "show ID column in active list output")
	fs.Var(&labels, "label", "include/exclude label rule; repeatable (+label, -label, or bare label)")
	fs.StringVar(&state, "state", "", "filter by state: backlog, working, waiting, archived")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	if len(fs.Args()) != 0 {
		return usageErr{errors.New("usage: wi list [--archived|--all] [--label <[+|-]label>]... [--state <state>] [--no-agent] [--ids]")}
	}
	state = strings.TrimSpace(state)
	if state != "" && state != model.StateBacklog && state != model.StateWorking && state != model.StateWaiting && state != model.StateArchived {
		return usageErr{fmt.Errorf("invalid state %q; expected backlog, working, waiting, or archived", state)}
	}
	if archived && all {
		return usageErr{errors.New("pass either --archived or --all, not both")}
	}
	if archived && state != "" && state != model.StateArchived {
		return usageErr{fmt.Errorf("--archived conflicts with --state %s", state)}
	}
	if state == model.StateArchived && !all {
		archived = true
	}
	labelRules, err := effectiveListLabelRules(cfg.App.ListConfig.Labels, cfg.Env, labels)
	if err != nil {
		return usageErr{err}
	}
	mode := "active"
	if archived {
		mode = "archived"
	}
	if all {
		mode = "all"
	}
	listOptions := app.WorkListOptions{ArchivedOnly: mode == "archived", IncludeArchived: mode == "all", LabelRules: labelRules, State: state}
	if !noAgent && mode != "archived" {
		barrierCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err = cfg.Coordinator.ActivityBarrier(barrierCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("refresh agent observations: %w", err)
		}
	}
	res := projectedWorkList(ctx, cfg, listOptions)
	agents := map[string]string(nil)
	if !noAgent && mode != "archived" {
		var agentWarnings []string
		agents, agentWarnings = enrichWorkListAgentStatus(ctx, cfg, &res)
		res.Warnings = append(res.Warnings, agentWarnings...)
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, res)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(cfg.Stderr, "warning: %s\n", w)
	}
	currentID := ""
	if current, err := cfg.App.ResolveItem(ctx, app.ResolveOptions{CWD: cfg.CWD, Env: cfg.Env}); err == nil {
		currentID = current.ID
	}
	printWorkList(cfg.Stdout, res, mode, colorEnabled(cfg.Env, cfg.Stdout), agents, listDisplayOptions{ShowIDs: ids || mode != "active", CurrentID: currentID})
	return nil
}

type projectedManifestStore struct{ manifests []model.Manifest }

func (s projectedManifestStore) ListManifests() ([]model.Manifest, []error) {
	return append([]model.Manifest(nil), s.manifests...), nil
}

func projectedWorkList(ctx context.Context, cfg Config, options app.WorkListOptions) app.WorkListResult {
	result, err := daemonWorkList(ctx, cfg, options)
	if err != nil {
		result.Warnings = append(result.Warnings, "daemon manifest projection unavailable: "+err.Error())
	}
	return result
}

func daemonWorkList(ctx context.Context, cfg Config, options app.WorkListOptions) (app.WorkListResult, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	projection, err := cfg.Coordinator.ManifestProjection(queryCtx)
	if err != nil {
		return app.WorkListResult{}, err
	}
	result := viewapp.New(projectedManifestStore{manifests: projection.Manifests}).WorkList(options, cfg.App.DeepWorkConfig.MaxActive, cfg.App.ListConfig.RepositoryFolders)
	result.Warnings = append(result.Warnings, projection.Projection.Warnings...)
	if !projection.Projection.Fresh {
		result.Warnings = append(result.Warnings, "daemon manifest projection is stale")
	}
	return result, nil
}

type listDisplayOptions struct {
	ShowIDs   bool
	CurrentID string
}

func printWorkList(w io.Writer, res app.WorkListResult, mode string, color bool, agents map[string]string, opts listDisplayOptions) {
	fmt.Fprintf(w, "Deep work active: %d/%d\n", res.DeepWork.Active, res.DeepWork.Limit)
	showAgent := len(agents) > 0
	displaySections := workDisplaySections(res, mode, agents)
	ids := []string{}
	nameWidth := len("ITEM")
	labelWidth := len("LABELS")
	deepWidth := len("DEEP")
	agentWidth := len("AGENT")
	worktreeWidth := len("WORKTREE")
	showWorktree := false
	for _, section := range displaySections {
		for _, item := range section.Items {
			if opts.ShowIDs {
				ids = append(ids, item.ID)
			}
			if n := utf8.RuneCountInString(itemDisplayName(item)); n > nameWidth {
				nameWidth = n
			}
			if n := utf8.RuneCountInString(strings.Join(labelMarkers(item), "  ")); n > labelWidth {
				labelWidth = n
			}
			if showAgent {
				if n := utf8.RuneCountInString(agents[item.ID]); n > agentWidth {
					agentWidth = n
				}
			}
			if item.Worktree != nil {
				showWorktree = true
				if n := utf8.RuneCountInString(worktreeListLabel(item)); n > worktreeWidth {
					worktreeWidth = n
				}
			}
		}
	}
	if nameWidth < 24 {
		nameWidth = 24
	}
	if nameWidth > 48 {
		nameWidth = 48
	}
	if labelWidth > 36 {
		labelWidth = 36
	}
	prefixes := map[string]string{}
	if opts.ShowIDs {
		prefixes = model.UniqueIDPrefixes(ids, 6)
	}
	hasRows := false
	for _, section := range displaySections {
		if len(section.Items) > 0 {
			hasRows = true
			break
		}
	}
	if !hasRows {
		fmt.Fprintln(w, "\nno work items")
		return
	}
	fmt.Fprintln(w)
	idHeader := listHeaderCell("ID", 10, color)
	itemHeader := listHeaderCell("ITEM", nameWidth, color)
	agentHeader := listHeaderCell("AGENT", agentWidth, color)
	worktreeHeader := listHeaderCell("WORKTREE", worktreeWidth, color)
	deepHeader := listHeaderCell("DEEP", deepWidth, color)
	labelsHeader := listHeaderCell("LABELS", labelWidth, color)
	repoHeader := colorBold("REPOSITORY", color)
	if showAgent {
		if opts.ShowIDs {
			if showWorktree {
				fmt.Fprintf(w, "%s %s %s  %s  %s  %s  %s  %s\n", " ", idHeader, itemHeader, agentHeader, worktreeHeader, deepHeader, labelsHeader, repoHeader)
			} else {
				fmt.Fprintf(w, "%s %s %s  %s  %s  %s  %s\n", " ", idHeader, itemHeader, agentHeader, deepHeader, labelsHeader, repoHeader)
			}
		} else if showWorktree {
			fmt.Fprintf(w, "%s %s  %s  %s  %s  %s  %s\n", " ", itemHeader, agentHeader, worktreeHeader, deepHeader, labelsHeader, repoHeader)
		} else {
			fmt.Fprintf(w, "%s %s  %s  %s  %s  %s\n", " ", itemHeader, agentHeader, deepHeader, labelsHeader, repoHeader)
		}
	} else {
		if opts.ShowIDs {
			fmt.Fprintf(w, "%s %s %s  %s  %s  %s\n", " ", idHeader, itemHeader, deepHeader, labelsHeader, repoHeader)
		} else {
			fmt.Fprintf(w, "%s %s  %s  %s  %s\n", " ", itemHeader, deepHeader, labelsHeader, repoHeader)
		}
	}
	fmt.Fprintln(w)
	firstSection := true
	printSection := func(name string, items []app.WorkListItem) {
		if len(items) == 0 {
			return
		}
		sectionName := colorBold(name, color)
		if firstSection {
			fmt.Fprintf(w, "%s\n", sectionName)
			firstSection = false
		} else {
			fmt.Fprintf(w, "\n%s\n", sectionName)
		}
		for _, item := range items {
			marker := " "
			current := opts.CurrentID != "" && item.ID == opts.CurrentID
			if current {
				marker = "›"
			}
			display := truncateRunes(itemDisplayName(item), nameWidth)
			displayCell := padRight(display, nameWidth)
			if current {
				displayCell = colorBold(displayCell, color)
			}
			repository := item.Repository
			if item.RepositoryHome {
				repository += " (" + colorBold("home", color) + ")"
			}
			worktree := padRight(worktreeListLabel(item), worktreeWidth)
			deep := formatDeepWorkColumn(item.DeepWork, deepWidth)
			labels := formatLabelColumn(labelMarkers(item), labelWidth, color)
			if showAgent {
				if opts.ShowIDs {
					if showWorktree {
						fmt.Fprintf(w, "%s %-10s %s  %s  %s  %s  %s  %s\n", marker, prefixes[item.ID], displayCell, padRight(agents[item.ID], agentWidth), worktree, deep, labels, repository)
					} else {
						fmt.Fprintf(w, "%s %-10s %s  %s  %s  %s  %s\n", marker, prefixes[item.ID], displayCell, padRight(agents[item.ID], agentWidth), deep, labels, repository)
					}
				} else if showWorktree {
					fmt.Fprintf(w, "%s %s  %s  %s  %s  %s  %s\n", marker, displayCell, padRight(agents[item.ID], agentWidth), worktree, deep, labels, repository)
				} else {
					fmt.Fprintf(w, "%s %s  %s  %s  %s  %s\n", marker, displayCell, padRight(agents[item.ID], agentWidth), deep, labels, repository)
				}
			} else {
				if opts.ShowIDs {
					fmt.Fprintf(w, "%s %-10s %s  %s  %s  %s\n", marker, prefixes[item.ID], displayCell, deep, labels, repository)
				} else {
					fmt.Fprintf(w, "%s %s  %s  %s  %s\n", marker, displayCell, deep, labels, repository)
				}
			}
		}
	}
	for _, section := range displaySections {
		printSection(section.Name, section.Items)
	}
}

func workDisplaySections(res app.WorkListResult, mode string, agents map[string]string) []workDisplaySection {
	if len(agents) == 0 {
		sections := []workDisplaySection{{Name: "WORKING", Items: res.Sections.Working}, {Name: "WAITING", Items: res.Sections.Waiting}, {Name: "BACKLOG", Items: res.Sections.Backlog}}
		if mode == "all" || mode == "archived" {
			sections = append(sections, workDisplaySection{Name: "ARCHIVED", Items: res.Sections.Archived})
		}
		return sections
	}
	needsAttention := []app.WorkListItem{}
	needsFixing := []app.WorkListItem{}
	inProgress := []app.WorkListItem{}
	waiting := []app.WorkListItem{}
	for _, item := range append(append([]app.WorkListItem{}, res.Sections.Working...), res.Sections.Waiting...) {
		bucket := workItemBucket(item, agents)
		if bucket == "needs_fixing" {
			needsFixing = append(needsFixing, item)
			continue
		}
		if item.State == model.StateWaiting {
			waiting = append(waiting, item)
			continue
		}
		switch bucket {
		case "in_progress":
			inProgress = append(inProgress, item)
		default:
			needsAttention = append(needsAttention, item)
		}
	}
	sort.SliceStable(needsAttention, func(i, j int) bool {
		if needsAttention[i].AttentionRank != needsAttention[j].AttentionRank {
			return needsAttention[i].AttentionRank < needsAttention[j].AttentionRank
		}
		return needsAttention[i].ID < needsAttention[j].ID
	})
	sections := []workDisplaySection{
		{Name: "NEEDS ATTENTION", Items: needsAttention},
		{Name: "NEEDS FIXING", Items: needsFixing},
		{Name: "IN PROGRESS", Items: inProgress},
		{Name: "WAITING", Items: waiting},
		{Name: "BACKLOG", Items: res.Sections.Backlog},
	}
	if mode == "all" || mode == "archived" {
		sections = append(sections, workDisplaySection{Name: "ARCHIVED", Items: res.Sections.Archived})
	}
	return sections
}

func workItemBucket(item app.WorkListItem, agents map[string]string) string {
	if item.Worktree != nil && item.Worktree.Status == "problem" {
		return "needs_fixing"
	}
	if item.Agent != nil && item.Agent.Bucket != "" {
		return item.Agent.Bucket
	}
	return agentListBucket(agents[item.ID])
}

func agentBaseStatus(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	base, _, _ := strings.Cut(label, "/")
	return base
}

func itemDisplayName(item app.WorkListItem) string {
	if item.Slug != "" {
		return item.Slug
	}
	return item.Title
}

func listHeaderCell(label string, width int, color bool) string {
	return colorBold(padRight(label, width), color)
}

func worktreeListLabel(item app.WorkListItem) string {
	if item.Worktree == nil {
		return ""
	}
	return item.Worktree.Status
}

func labelMarkers(item app.WorkListItem) []string {
	markers := []string{}
	if item.CapacityFull {
		markers = append(markers, "capacity full")
	}
	markers = append(markers, item.Labels...)
	return markers
}

func formatDeepWorkColumn(deep bool, width int) string {
	marker := ""
	if deep {
		marker = "●"
	}
	return padRight(marker, width)
}

func formatLabelColumn(markers []string, width int, color bool) string {
	raw := strings.Join(markers, "  ")
	if utf8.RuneCountInString(raw) <= width {
		colored := make([]string, 0, len(markers))
		for _, marker := range markers {
			colored = append(colored, colorLabelMarker(marker, marker, color))
		}
		return strings.Join(colored, "  ") + strings.Repeat(" ", width-utf8.RuneCountInString(raw))
	}
	if width <= 0 {
		return ""
	}
	limit := width
	ellipsis := ""
	if width > 3 {
		limit = width - 3
		ellipsis = "..."
	}
	parts := []string{}
	visible := 0
	for i, marker := range markers {
		if visible >= limit {
			break
		}
		if i > 0 {
			sep := "  "
			sepWidth := utf8.RuneCountInString(sep)
			if visible+sepWidth > limit {
				break
			}
			parts = append(parts, sep)
			visible += sepWidth
		}
		remaining := limit - visible
		if remaining <= 0 {
			break
		}
		piece := marker
		if utf8.RuneCountInString(piece) > remaining {
			piece = string([]rune(piece)[:remaining])
		}
		parts = append(parts, colorLabelMarker(piece, marker, color))
		visible += utf8.RuneCountInString(piece)
		if piece != marker {
			break
		}
	}
	visible += utf8.RuneCountInString(ellipsis)
	return strings.Join(parts, "") + ellipsis + strings.Repeat(" ", width-visible)
}

func padRight(s string, width int) string {
	count := utf8.RuneCountInString(s)
	if count >= width {
		return s
	}
	return s + strings.Repeat(" ", width-count)
}

func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	if max <= 3 {
		r := []rune(s)
		return string(r[:max])
	}
	r := []rune(s)
	return string(r[:max-3]) + "..."
}

func colorEnabled(env map[string]string, w io.Writer) bool {
	if env == nil {
		return false
	}
	if env["NO_COLOR"] != "" || env["TERM"] == "" || env["TERM"] == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func colorLabelMarker(text, marker string, enabled bool) string {
	if marker == "capacity full" {
		return colorWarning(text, enabled)
	}
	return colorLabelWithKey(text, marker, enabled)
}

func colorLabelWithKey(label, key string, enabled bool) string {
	if !enabled || label == "" {
		return label
	}
	palette := []int{31, 32, 33, 34, 35, 36, 91, 92, 93, 94, 95, 96}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	code := palette[int(h.Sum32())%len(palette)]
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", code, label)
}

func colorWarning(s string, enabled bool) string {
	if !enabled || s == "" {
		return s
	}
	return "\x1b[31m" + s + "\x1b[0m"
}

func colorBold(s string, enabled bool) string {
	if !enabled || s == "" {
		return s
	}
	return "\x1b[1m" + s + "\x1b[0m"
}
