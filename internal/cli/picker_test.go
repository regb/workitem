package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"github.com/rivo/uniseg"
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
	cfg := Config{Stderr: &bytes.Buffer{}, Env: map[string]string{"WI_FZF": script, "ARGS_PATH": argsPath}}
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

func TestPickerDisplayCellUsesTerminalCellsAndWholeGraphemes(t *testing.T) {
	for _, test := range []struct {
		value string
		width int
		want  string
	}{{"abcdef", 4, "abc…"}, {"猫猫猫", 4, "猫… "}, {"e\u0301cole", 3, "e\u0301c…"}, {"👩‍💻 coding", 4, "👩‍💻 …"}, {"title", 0, ""}} {
		got := pickerDisplayCell(test.value, test.width)
		if got != test.want || uniseg.StringWidth(got) != test.width {
			t.Fatalf("%q width=%d got=%q", test.value, test.width, got)
		}
	}
}

func TestPickerPaneColumnsRespondToPreviewGeometry(t *testing.T) {
	for _, test := range []struct {
		env  map[string]string
		want int
	}{
		{map[string]string{"FZF_COLUMNS": "200", "FZF_PREVIEW_LEFT": "122", "FZF_PREVIEW_COLUMNS": "76"}, 117},
		{map[string]string{"FZF_COLUMNS": "160", "FZF_PREVIEW_LEFT": "98", "FZF_PREVIEW_COLUMNS": "60"}, 93},
		{map[string]string{"FZF_COLUMNS": "120", "FZF_PREVIEW_LEFT": "2", "FZF_PREVIEW_COLUMNS": "116"}, 118},
		{map[string]string{"FZF_COLUMNS": "200"}, 198},
	} {
		if got := pickerPaneColumns(test.env); got != test.want {
			t.Fatalf("env=%v got=%d want=%d", test.env, got, test.want)
		}
	}
}

func TestPickerRowsResizeOnlyTitleAndPreserveFullSearchField(t *testing.T) {
	candidates := []pickerCandidate{
		{Section: "NEEDS ATTENTION", Current: true, Item: app.WorkListItem{ID: "one", Title: strings.Repeat("Long title ", 20) + "suffix", Repository: "longest-repository"}},
		{Section: "WAITING", Item: app.WorkListItem{ID: "two", Title: "Short title", Repository: "short"}},
	}
	snapshot := pickerSnapshot(candidates)
	for _, columns := range []int{64, 94, 118, 198} {
		var out strings.Builder
		if err := snapshot.rows(&out, columns); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		header := pickerPlainRow(snapshot.header(columns))
		repoColumn := strings.Index(header, "REPOSITORY")
		for i, line := range lines {
			fields := strings.Split(line, "\t")
			display := pickerPlainRow(fields[1])
			repo := candidates[i].Item.Repository
			if uniseg.StringWidth(display) != columns+2 || strings.Index(display, repo) < 0 || uniseg.StringWidth(display[:strings.Index(display, repo)]) != repoColumn {
				t.Fatalf("columns=%d header=%q row=%q", columns, header, display)
			}
			if !strings.Contains(fields[2], candidates[i].Item.Title) {
				t.Fatalf("native title lost: %q", fields)
			}
		}
	}
}

func TestPrivatePickerFormatterReadsOnlyFrozenSnapshot(t *testing.T) {
	self := "/path with spaces/wi"
	snapshot := pickerSnapshot([]pickerCandidate{{Section: "WAITING", Item: app.WorkListItem{ID: "stable-id", Title: "Title with (parentheses)", Repository: "project"}}})
	path := filepath.Join(t.TempDir(), "snapshot.json")
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FZF_COLUMNS": "100"}
	var out, stderr bytes.Buffer
	if code := runPickerRender([]string{path, self}, env, &out, &stderr); code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "change-header(") || !strings.Contains(out.String(), "reload-sync("+shellQuotePicker(self)) || strings.Contains(out.String(), "Title with") {
		t.Fatalf("unsafe action expression: %q", out.String())
	}
	out.Reset()
	if code := runPickerRender([]string{"--rows", path, self}, env, &out, &stderr); code != ExitOK || !strings.HasPrefix(out.String(), "stable-id\t") {
		t.Fatalf("code=%d out=%q stderr=%s", code, out.String(), stderr.String())
	}
	for _, args := range [][]string{nil, {"bad"}, {path}, {"--rows", path}, {path, self, "extra"}} {
		if code := runPickerRender(args, env, &out, &stderr); code == ExitOK {
			t.Fatalf("accepted args=%v", args)
		}
	}
	if err := os.WriteFile(path, []byte(`{"StatusWidth":-1,"RepositoryWidth":9}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := runPickerRender([]string{path, self}, env, &out, &stderr); code == ExitOK {
		t.Fatal("accepted malformed layout")
	}
}

func TestPickerKeepsWaitingVisibleAlongsideAgentStatus(t *testing.T) {
	dependency := "Remove deprecated Android window APIs and verify navigation modes"
	for _, section := range []string{"WAITING", "NEEDS ATTENTION", "IN PROGRESS", "NEEDS FIXING"} {
		t.Run(section, func(t *testing.T) {
			candidates := []pickerCandidate{
				{Section: section, Item: app.WorkListItem{ID: "winsmash", Title: "winsmash", Repository: "winsmash", Waiting: true, Unread: section == "NEEDS ATTENTION", Blockers: []model.Blocker{{ID: "dependency", Kind: model.BlockerItem, ItemID: "android", CreatedAt: time.Now()}}}},
				{Section: "IN PROGRESS", Item: app.WorkListItem{ID: "android", Title: dependency, Repository: "sgl"}},
			}
			snapshot := pickerSnapshot(candidates)
			for _, columns := range []int{48, 64, 78, 94, 118, 198} {
				var out strings.Builder
				if err := snapshot.rows(&out, columns); err != nil {
					t.Fatal(err)
				}
				fields := strings.Split(strings.Split(out.String(), "\n")[0], "\t")
				visible := pickerPlainRow(fields[1])
				if !strings.Contains(fields[1], pickerYellow+" · ") || strings.Contains(visible, "waiting: ") {
					t.Fatalf("columns=%d lost condition label or added redundant prefix: %q", columns, visible)
				}
				status, _ := pickerSectionStyle(section)
				if !strings.Contains(visible, status) {
					t.Fatal("waiting indicator replaced agent status")
				}
				if uniseg.StringWidth(visible) != columns+2 {
					t.Fatalf("columns=%d row shifted metadata: %q", columns, visible)
				}
				if !strings.Contains(fields[2], dependency) || !strings.Contains(fields[2], "waiting") {
					t.Fatal("truncated dependency is not searchable")
				}
				if candidates[0].Item.Unread && !strings.Contains(fields[1], pickerBold+"winsmash") {
					t.Fatal("waiting marker removed unread emphasis")
				}
			}
		})
	}
}

func TestPickerNameAndWaitingShareAllAvailableSpace(t *testing.T) {
	for _, test := range []struct{ name, title, condition, suffix string }{
		{"short name borrows for long label", "winsmash", "on remove-deprecated-android-window-apis", " · on remove-deprecated-android-window-apis"},
		{"long name borrows for short label", "Surface product assumptions and testing requirements", "waiting", " · waiting"},
		{"short specific label", "Long descriptive work item name with useful details", "review", " · review"},
		{"wide graphemes", "猫👩‍💻", "on 猫猫猫 é implementation review and verification", " · on 猫猫猫 é implementation review and verification"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := pickerSnapshot([]pickerCandidate{{Section: "WAITING", Item: app.WorkListItem{ID: "one", Title: test.title, Repository: "project", Blockers: []model.Blocker{{Kind: model.BlockerExternal, Event: test.condition}}}}})
			naturalWidth := uniseg.StringWidth(test.title + test.suffix)
			for _, extra := range []int{0, 1, 5, 20} {
				columns := snapshot.StatusWidth + snapshot.RepositoryWidth + 6 + naturalWidth + extra
				var out strings.Builder
				if err := snapshot.rows(&out, columns); err != nil {
					t.Fatal(err)
				}
				fields := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\t")
				visible := pickerPlainRow(fields[1])
				if !strings.Contains(visible, test.title+test.suffix) || strings.Contains(visible, "…") {
					t.Fatalf("unused space caused truncation at %d cells: %q", columns, visible)
				}
				if uniseg.StringWidth(visible) != columns+2 {
					t.Fatal("dynamic allocation moved repository column")
				}
			}
		})
	}
}

func TestPickerWaitingLabelResizesWithoutSplittingGraphemes(t *testing.T) {
	snapshot := pickerSnapshot([]pickerCandidate{{Section: "IN PROGRESS", Item: app.WorkListItem{ID: "blocked", Title: "Long title that yields space to its blocker", Repository: "project", Waiting: true, Blockers: []model.Blocker{{Kind: model.BlockerExternal, Event: "Review 猫👩‍💻 é implementation"}}}}})
	for _, columns := range []int{40, 64, 80, 120} {
		var out strings.Builder
		if err := snapshot.rows(&out, columns); err != nil {
			t.Fatal(err)
		}
		fields := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\t")
		visible := pickerPlainRow(fields[1])
		if !strings.Contains(fields[1], pickerYellow+" · ") || uniseg.StringWidth(visible) != columns+2 {
			t.Fatalf("columns=%d row=%q", columns, visible)
		}
	}
}
