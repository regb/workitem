package cli

// Presentation-only list rendering retained from the pre-cutover client.
// All input is supplied by API responses; this file performs no inspection.
import (
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

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
	Key   string
	Name  string
	Items []app.WorkListItem
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
	resolutionWidth := len("RESOLUTION")
	showWorktree, showResolution := false, false
	slugs := listSlugs(res)
	for _, section := range displaySections {
		for _, item := range section.Items {
			if opts.ShowIDs {
				ids = append(ids, item.ID)
			}
			if n := utf8.RuneCountInString(itemDisplayName(item) + waitingSuffix(item, slugs)); n > nameWidth {
				nameWidth = n
			}
			if n := utf8.RuneCountInString(strings.Join(listLabelMarkers(item, color), "  ")); n > labelWidth {
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
			if item.Resolution != "" {
				showResolution = true
				if n := utf8.RuneCountInString(item.Resolution); n > resolutionWidth {
					resolutionWidth = n
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
	// Rows are a marker, an optional ID prefix, then two-space separated
	// columns; REPOSITORY is last and unpadded.
	header := []string{listHeaderCell("ITEM", nameWidth, color)}
	if showResolution {
		header = append(header, listHeaderCell("RESOLUTION", resolutionWidth, color))
	}
	if showAgent {
		header = append(header, listHeaderCell("AGENT", agentWidth, color))
		if showWorktree {
			header = append(header, listHeaderCell("WORKTREE", worktreeWidth, color))
		}
	}
	header = append(header, listHeaderCell("DEEP", deepWidth, color), listHeaderCell("LABELS", labelWidth, color))
	header = append(header, colorBold("REPOSITORY", color))
	printRow := func(marker, id string, cells []string) {
		prefix := marker + " "
		if opts.ShowIDs {
			prefix += id + " "
		}
		fmt.Fprintln(w, prefix+strings.Join(cells, "  "))
	}
	printRow(" ", listHeaderCell("ID", 10, color), header)
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
			// What an item waits for follows its name in yellow; the name is
			// truncated first so the waiting label stays visible.
			suffix := waitingSuffix(item, slugs)
			name := truncateRunes(itemDisplayName(item), max(8, nameWidth-utf8.RuneCountInString(suffix)))
			padding := strings.Repeat(" ", max(0, nameWidth-utf8.RuneCountInString(name+suffix)))
			// Unread responses are bold, like unread mail; the current item is
			// bold as well and keeps its › marker.
			if current || item.Unread {
				name = colorBold(name, color)
			}
			displayCell := name + colorWaiting(suffix, color) + padding
			cells := []string{displayCell}
			if showResolution {
				cells = append(cells, padRight(item.Resolution, resolutionWidth))
			}
			if showAgent {
				cells = append(cells, padRight(agents[item.ID], agentWidth))
				if showWorktree {
					cells = append(cells, padRight(worktreeListLabel(item), worktreeWidth))
				}
			}
			cells = append(cells, formatDeepWorkColumn(item.DeepWork, deepWidth), formatLabelColumn(listLabelMarkers(item, color), labelWidth, color))
			cells = append(cells, item.Repository)
			printRow(marker, fmt.Sprintf("%-10s", prefixes[item.ID]), cells)
		}
	}
	for _, section := range displaySections {
		printSection(section.Name, section.Items)
	}
}

func workDisplaySections(res app.WorkListResult, mode string, agents map[string]string) []workDisplaySection {
	if len(agents) == 0 {
		sections := []workDisplaySection{{Key: "active", Name: "ACTIVE", Items: res.Sections.Active}, {Key: "waiting", Name: "WAITING", Items: res.Sections.Waiting}, {Key: "backlog", Name: "BACKLOG", Items: res.Sections.Backlog}}
		if mode == "all" || mode == "archived" {
			sections = append(sections, workDisplaySection{Key: "closed", Name: "CLOSED", Items: res.Sections.Closed})
		}
		return sections
	}
	needsAttention := []app.WorkListItem{}
	needsFixing := []app.WorkListItem{}
	inProgress := []app.WorkListItem{}
	waiting := []app.WorkListItem{}
	for _, item := range append(append([]app.WorkListItem{}, res.Sections.Active...), res.Sections.Waiting...) {
		bucket := workItemBucket(item, agents)
		if bucket == "needs_fixing" {
			needsFixing = append(needsFixing, item)
			continue
		}
		// A busy agent is working on something the operator asked for, so a
		// waiting item shows IN PROGRESS; once done, its unread response keeps
		// it in NEEDS ATTENTION until read, then it returns to WAITING.
		if item.Waiting && item.AttentionRank == 0 && bucket != "in_progress" {
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
		{Key: "needs_attention", Name: "NEEDS ATTENTION", Items: needsAttention},
		{Key: "needs_fixing", Name: "NEEDS FIXING", Items: needsFixing},
		{Key: "in_progress", Name: "IN PROGRESS", Items: inProgress},
		{Key: "waiting", Name: "WAITING", Items: waiting},
		{Key: "backlog", Name: "BACKLOG", Items: res.Sections.Backlog},
	}
	if mode == "all" || mode == "archived" {
		sections = append(sections, workDisplaySection{Key: "closed", Name: "CLOSED", Items: res.Sections.Closed})
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

// maxWaitingSuffix bounds the waiting label shown after an item name.
const maxWaitingSuffix = 24

// waitingSuffix is the compact waiting label shown after an item's name.
func waitingSuffix(item app.WorkListItem, slugs map[string]string) string {
	if len(item.Blockers) == 0 {
		return ""
	}
	text := model.ShortList(item.Blockers, time.Now(), func(id string) string { return slugs[id] })
	return "  · " + truncateRunes(text, maxWaitingSuffix)
}

// listSlugs names every listed item so item blockers can show a slug.
func listSlugs(res app.WorkListResult) map[string]string {
	slugs := map[string]string{}
	for _, items := range [][]app.WorkListItem{res.Sections.Active, res.Sections.Waiting, res.Sections.Backlog, res.Sections.Closed} {
		for _, item := range items {
			slugs[item.ID] = itemDisplayName(item)
		}
	}
	return slugs
}

// listLabelMarkers adds a textual unread marker when color, and therefore
// bold titles, are unavailable.
func listLabelMarkers(item app.WorkListItem, color bool) []string {
	markers := labelMarkers(item)
	if item.Unread && !color {
		markers = append([]string{"unread"}, markers...)
	}
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

// colorWaiting calls out what an item waits for.
func colorWaiting(s string, enabled bool) string {
	if !enabled || s == "" {
		return s
	}
	return "\x1b[33m" + s + "\x1b[0m"
}

func colorBold(s string, enabled bool) string {
	if !enabled || s == "" {
		return s
	}
	return "\x1b[1m" + s + "\x1b[0m"
}
