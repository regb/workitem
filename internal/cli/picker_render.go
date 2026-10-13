package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/rivo/uniseg"
)

// This temporary, read-only snapshot belongs to one open picker, not the daemon.
type pickerRenderItem struct {
	ID, Title, Repository, Section string
	Current                        bool
}
type pickerRenderSnapshot struct {
	Items                        []pickerRenderItem
	StatusWidth, RepositoryWidth int
}

func pickerSnapshot(candidates []pickerCandidate) pickerRenderSnapshot {
	status, repo := pickerColumnWidths(candidates)
	snapshot := pickerRenderSnapshot{StatusWidth: status, RepositoryWidth: repo}
	for _, candidate := range candidates {
		title := pickerText(candidate.Item.Title)
		if title == "" {
			title = pickerText(candidate.Item.Slug)
		}
		snapshot.Items = append(snapshot.Items, pickerRenderItem{ID: candidate.Item.ID, Title: title, Repository: pickerText(candidate.Item.Repository), Section: candidate.Section, Current: candidate.Current})
	}
	return snapshot
}

func pickerDisplayCell(value string, width int) string {
	value = pickerText(value)
	width = max(0, width)
	if uniseg.StringWidth(value) > width {
		if width == 0 {
			return ""
		}
		var out strings.Builder
		graphemes := uniseg.NewGraphemes(value)
		used := 0
		for graphemes.Next() {
			w := graphemes.Width()
			if used+w > width-1 {
				break
			}
			out.WriteString(graphemes.Str())
			used += w
		}
		value = out.String() + "…"
	}
	return value + strings.Repeat(" ", max(0, width-uniseg.StringWidth(value)))
}

func (snapshot pickerRenderSnapshot) header(columns int) string {
	titleWidth := max(0, columns-snapshot.StatusWidth-snapshot.RepositoryWidth-6)
	return pickerBold + "  " + pickerDisplayCell("STATUS", snapshot.StatusWidth) + "  " + pickerDisplayCell("ITEM", titleWidth) + "  " + pickerDisplayCell("REPOSITORY", snapshot.RepositoryWidth) + pickerReset
}

func (snapshot pickerRenderSnapshot) rows(out io.Writer, columns int) error {
	titleWidth := max(0, columns-snapshot.StatusWidth-snapshot.RepositoryWidth-6)
	for _, item := range snapshot.Items {
		marker := "  "
		if item.Current {
			marker = pickerBold + pickerMagenta + "● " + pickerReset
		}
		row := marker + pickerSectionBadge(item.Section, snapshot.StatusWidth) + "  " + pickerBold + pickerDisplayCell(item.Title, titleWidth) + pickerReset + "  " + pickerDim + pickerDisplayCell(item.Repository, snapshot.RepositoryWidth) + pickerReset
		// The search-only field starts beyond the visible pane. --no-hscroll and
		// --no-wrap keep it off-screen; --nth searches its complete native text.
		label, _ := pickerSectionStyle(item.Section)
		if _, err := fmt.Fprintf(out, "%s\t%s  \t%s %s %s %s\n", item.ID, row, item.Title, item.Repository, pickerText(item.Section), label); err != nil {
			return err
		}
	}
	return nil
}

func pickerPaneColumns(env map[string]string) int {
	columns, _ := strconv.Atoi(env["FZF_COLUMNS"])
	if columns <= 0 {
		columns = 80
	}
	left, _ := strconv.Atoi(env["FZF_PREVIEW_LEFT"])
	previewColumns, _ := strconv.Atoi(env["FZF_PREVIEW_COLUMNS"])
	if previewColumns > 0 && left > columns/2 {
		// Rounded preview borders and the list/preview separator occupy
		// three cells before the preview content. The list pointer/gutter
		// consumes another two cells (--scrollbar=).
		columns = left - 3
	}
	return min(16384, max(1, columns-2))
}

// runPickerRender is a private, stateless fzf formatting callback. It reads
// only the frozen temporary snapshot and never opens application state.
func runPickerRender(args []string, env map[string]string, out, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "render picker:", err); return ExitError }
	rows := len(args) == 3 && args[0] == "--rows"
	if (!rows && len(args) != 2) || (rows && len(args) != 3) {
		return fail(fmt.Errorf("invalid formatter arguments"))
	}
	path, self := args[0], args[1]
	if rows {
		path, self = args[1], args[2]
	}
	file, err := os.Open(path)
	if err != nil {
		return fail(err)
	}
	defer file.Close()
	var snapshot pickerRenderSnapshot
	if err := json.NewDecoder(io.LimitReader(file, 32<<20)).Decode(&snapshot); err != nil {
		return fail(err)
	}
	if snapshot.StatusWidth < len("STATUS") || snapshot.StatusWidth > 512 || snapshot.RepositoryWidth < len("REPOSITORY") || snapshot.RepositoryWidth > 512 {
		return fail(fmt.Errorf("invalid snapshot column widths"))
	}
	columns := pickerPaneColumns(env)
	if rows {
		if err := snapshot.rows(out, columns); err != nil {
			return fail(err)
		}
		return ExitOK
	}
	command := shellQuotePicker(self) + " internal-picker-render --rows " + shellQuotePicker(path) + " " + shellQuotePicker(self)
	header := pickerDim + "FROZEN SNAPSHOT · reopen the picker to refresh" + pickerReset + "\n" + snapshot.header(columns)
	// Labels and numeric widths are the only data in the action expression;
	// native titles/repository names go through the separately quoted reload.
	fmt.Fprintf(out, "track-current+change-header(%s)+reload-sync(%s)\n", header, command)
	return ExitOK
}
