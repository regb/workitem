package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/itemnotes"
	"github.com/regb/workitem/internal/model"
)

func runSearch(ctx context.Context, args []string, cfg Config, jsonOut bool) error {
	fs := newFlagSet("search", cfg.Stderr)
	var state string
	var limit int
	fs.StringVar(&state, "state", "", "only backlog, active, or closed items")
	fs.IntVar(&limit, "limit", viewapp.DefaultSearchLimit, "most results to show")
	if err := fs.Parse(args); err != nil {
		return usageErr{err}
	}
	query := strings.Join(fs.Args(), " ")
	if len(viewapp.SearchTerms(query)) == 0 {
		return usageErr{errors.New("usage: wi search [--state <state>] [--limit <n>] <query>")}
	}
	if state != "" && state != model.StateBacklog && state != model.StateActive && state != model.StateClosed {
		return usageErr{fmt.Errorf("--state must be backlog, active, or closed")}
	}
	if limit < 1 {
		return usageErr{errors.New("--limit must be at least 1")}
	}
	result, err := searchItems(ctx, cfg, viewapp.SearchOptions{Query: query, State: state, Limit: limit})
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(cfg.Stdout, result)
	}
	printSearchResult(cfg, result)
	return nil
}

// searchItems searches the daemon's items, reading descriptions and notes
// from their item directories.
func searchItems(ctx context.Context, cfg Config, opts viewapp.SearchOptions) (viewapp.SearchResult, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	projection, err := cfg.Coordinator.ManifestProjection(queryCtx)
	if err != nil {
		return viewapp.SearchResult{}, err
	}
	result := viewapp.New(projectedManifestStore{manifests: projection.Manifests}).Search(opts, cfg.App.ListConfig.RepositoryFolders, func(m model.Manifest) viewapp.SearchText {
		return itemSearchText(cfg.App.Store.ItemDir(m.ID))
	})
	result.Warnings = append(result.Warnings, projection.Projection.Warnings...)
	if !projection.Projection.Fresh {
		result.Warnings = append(result.Warnings, "daemon manifest projection is stale")
	}
	return result, nil
}

// itemSearchText reads an item's description and notes. An unreadable one
// is searched as empty: search is no place to report a damaged item.
func itemSearchText(dir string) viewapp.SearchText {
	text := viewapp.SearchText{}
	if description, err := os.ReadFile(filepath.Join(dir, model.DescriptionFilename)); err == nil {
		text.Description = string(description)
	}
	notes, _ := itemnotes.Read(dir)
	for _, note := range notes {
		if note.Attachment != nil {
			text.Notes = append(text.Notes, note.Attachment.Name)
		}
		text.Notes = append(text.Notes, note.Text)
	}
	return text
}

func printSearchResult(cfg Config, result viewapp.SearchResult) {
	for _, warning := range result.Warnings {
		fmt.Fprintf(cfg.Stderr, "warning: %s\n", warning)
	}
	if len(result.Items) == 0 {
		fmt.Fprintln(cfg.Stdout, "no matching work items")
		return
	}
	ids := []string{}
	for _, hit := range result.Items {
		ids = append(ids, hit.ID)
	}
	prefixes := model.UniqueIDPrefixes(ids, 6)
	// Excerpts go on their own indented line, so they do not widen the
	// columns.
	rows := [][]string{{"ITEM", "STATE", "TITLE", "REPOSITORY", "MATCH"}}
	for _, hit := range result.Items {
		state := hit.State
		if hit.Resolution != "" {
			state += " (" + hit.Resolution + ")"
		}
		rows = append(rows, []string{prefixes[hit.ID], state, hit.Title, hit.Repository, hit.Field})
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], len([]rune(cell)))
		}
	}
	for index, row := range rows {
		line := ""
		for i, cell := range row {
			if i < len(row)-1 {
				cell += strings.Repeat(" ", widths[i]-len([]rune(cell))+2)
			}
			line += cell
		}
		fmt.Fprintln(cfg.Stdout, line)
		if index > 0 && result.Items[index-1].Excerpt != "" {
			fmt.Fprintf(cfg.Stdout, "%s%s\n", strings.Repeat(" ", widths[0]+2), result.Items[index-1].Excerpt)
		}
	}
	if result.Total > len(result.Items) {
		fmt.Fprintf(cfg.Stdout, "%d more; narrow the query or raise --limit\n", result.Total-len(result.Items))
	}
}
