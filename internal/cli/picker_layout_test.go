package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func pickerPlainRow(value string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(value, "")
}

func TestPickerItemUsesFullTitleWithoutSlug(t *testing.T) {
	title := strings.Repeat("Long title ", 12) + "searchable suffix"
	slug := strings.Repeat("long-slug-", 8) + "slug-suffix"
	snapshot := pickerSnapshot([]pickerCandidate{{Section: "BACKLOG", Item: app.WorkListItem{ID: "one", Title: title, Slug: slug, Repository: "project"}}})
	var output strings.Builder
	if err := snapshot.rows(&output, 80); err != nil {
		t.Fatal(err)
	}
	row := pickerPlainRow(output.String())
	fields := strings.Split(row, "\t")
	if len(fields) != 3 || !strings.Contains(fields[2], title) || strings.Contains(fields[1], "searchable suffix") || strings.Contains(row, slug) {
		t.Fatalf("truncated or reordered item: %q", row)
	}
	for _, item := range []app.WorkListItem{{Title: "same", Slug: "same"}, {Slug: "legacy-slug"}, {Title: "Only title"}} {
		snapshot := pickerSnapshot([]pickerCandidate{{Item: item}})
		var output strings.Builder
		if err := snapshot.rows(&output, 80); err != nil {
			t.Fatal(err)
		}
		fields := strings.Split(pickerPlainRow(output.String()), "\t")
		want := item.Title
		if want == "" {
			want = item.Slug
		}
		if !strings.Contains(fields[1], want) {
			t.Fatalf("missing title or legacy fallback: %q", fields)
		}
	}
}

func TestPickerColumnWidthsUseEntireSnapshot(t *testing.T) {
	longRepo := strings.Repeat("repository-", 5) + "suffix"
	candidates := []pickerCandidate{
		{Section: "NEEDS ATTENTION", Current: true, Item: app.WorkListItem{Title: "Small title", Repository: longRepo}},
		{Section: "WAITING", Item: app.WorkListItem{Title: strings.Repeat("Long title ", 20), Repository: "project-b"}},
	}
	statusWidth, repoWidth := pickerColumnWidths(candidates)
	if statusWidth != len("ATTENTION") || repoWidth != len(longRepo) {
		t.Fatalf("widths=%d,%d", statusWidth, repoWidth)
	}
	snapshot := pickerSnapshot(candidates)
	columns := 160
	header := pickerPlainRow(snapshot.header(columns))
	repoColumn := columns - repoWidth
	if strings.Index(header, "REPOSITORY") != repoColumn {
		t.Fatalf("header=%q", header)
	}
	// A filtered result still uses the original snapshot widths.
	for _, candidate := range candidates {
		snapshot.Items = pickerSnapshot([]pickerCandidate{candidate}).Items
		var output strings.Builder
		if err := snapshot.rows(&output, columns); err != nil {
			t.Fatal(err)
		}
		fields := strings.Split(pickerPlainRow(output.String()), "\t")
		row := fields[1][:len(fields[1])-2]
		if len([]rune(row)) != columns || len([]rune(row[:strings.Index(row, candidate.Item.Repository)])) != repoColumn {
			t.Fatalf("metadata moved: %q", row)
		}
	}
	shortStatus, shortRepo := pickerColumnWidths(candidates[1:])
	if shortStatus >= statusWidth || shortRepo >= repoWidth {
		t.Fatalf("fixture should distinguish filtered widths: %d,%d", shortStatus, shortRepo)
	}
}

func TestPickerClipsMiddleTitleAndRetainsFullSearchText(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fzf")
	argsPath := filepath.Join(dir, "args")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_PATH\"\nhead -n 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	candidates := []pickerCandidate{{Item: app.WorkListItem{ID: "selected-id", Title: "Visible title", Slug: "stable-slug"}}}
	cfg := Config{Stderr: &bytes.Buffer{}, Env: map[string]string{"WI_FZF": script, "ARGS_PATH": argsPath, "FZF_DEFAULT_OPTS": "--hscroll --wrap"}}
	if _, ok, err := selectWithFZF(context.Background(), cfg, candidates, true); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"--no-hscroll", "--no-wrap", "--nth=2..", "--with-nth=2..", "--id-nth=1", "--scrollbar="} {
		if !strings.Contains(string(data), arg+"\n") {
			t.Fatalf("missing %s: %s", arg, data)
		}
	}
}

func TestPickerPreviewAdaptsAndCanBeHidden(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fzf")
	argsPath := filepath.Join(dir, "args")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_PATH\"\nhead -n 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{App: &app.App{}, Stderr: &bytes.Buffer{}, Env: map[string]string{"WI_FZF": script, "ARGS_PATH": argsPath}}
	candidates := []pickerCandidate{{Item: app.WorkListItem{ID: "selected-id", Title: "Title", Slug: "slug"}}}
	for _, noPreview := range []bool{false, true} {
		if _, ok, err := selectWithFZF(context.Background(), cfg, candidates, noPreview); err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		data, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatal(err)
		}
		hasPreview := strings.Contains(string(data), "--preview-window=right:40%:border-rounded:wrap:<60(down:40%:border-rounded:wrap)\n")
		hasToggle := strings.Contains(string(data), "--bind=alt-p:toggle-preview+transform(")
		if hasPreview == noPreview || hasToggle == noPreview {
			t.Fatalf("noPreview=%v args=%s", noPreview, data)
		}
	}
}

func TestRealFZFFiltersFullTitleWithoutChangingSelectionIdentity(t *testing.T) {
	executable, err := exec.LookPath("fzf")
	if err != nil {
		t.Skip("fzf not installed")
	}
	help, err := exec.Command(executable, "--help").Output()
	if err != nil || !bytes.Contains(help, []byte("--id-nth")) {
		t.Skip("fzf 0.71+ not installed")
	}
	wrapper := filepath.Join(t.TempDir(), "filter-fzf")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \"$REAL_FZF\" --filter=\"$QUERY\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	candidate := pickerCandidate{Item: app.WorkListItem{ID: "stable-id", State: model.StateActive, Repository: "search-repository", Title: strings.Repeat("Long title ", 15) + "title-suffix-needle", Slug: "hidden-slug"}}
	for _, query := range []string{"title-suffix-needle", "search-repository"} {
		cfg := Config{Stderr: &bytes.Buffer{}, Env: map[string]string{"WI_FZF": wrapper, "REAL_FZF": executable, "QUERY": query, "FZF_DEFAULT_OPTS": "", "FZF_DEFAULT_OPTS_FILE": ""}}
		selected, ok, err := selectWithFZF(context.Background(), cfg, []pickerCandidate{candidate}, true)
		if err != nil || !ok || selected.Item.ID != candidate.Item.ID {
			t.Fatalf("query=%s selection=%+v ok=%v err=%v", query, selected, ok, err)
		}
	}
}
