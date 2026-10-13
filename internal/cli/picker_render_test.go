package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	"github.com/rivo/uniseg"
)

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
