package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
)

func TestPromptDirenvApprovalDefaultsToNo(t *testing.T) {
	for _, input := range []string{"\n", "no\n", "unexpected\n"} {
		var output bytes.Buffer
		approved, err := promptDirenvApproval(bytes.NewBufferString(input), &output, model.Manifest{Slug: "item"}, "/repo/.envrc")
		if err != nil || approved {
			t.Fatalf("input=%q approved=%v err=%v", input, approved, err)
		}
	}
}

func TestPromptDirenvApprovalAcceptsExplicitYes(t *testing.T) {
	var output bytes.Buffer
	approved, err := promptDirenvApproval(bytes.NewBufferString("yes\n"), &output, model.Manifest{Slug: "item"}, "/repo/.envrc")
	if err != nil || !approved {
		t.Fatalf("approved=%v err=%v", approved, err)
	}
}

func TestPickerCandidatesIncludeWorkingWaitingAndBacklog(t *testing.T) {
	res := app.WorkListResult{Sections: app.WorkListSections{
		Working:  []app.WorkListItem{{ID: "working", State: model.StateWorking}},
		Waiting:  []app.WorkListItem{{ID: "waiting", State: model.StateWaiting}},
		Backlog:  []app.WorkListItem{{ID: "backlog", State: model.StateBacklog}},
		Archived: []app.WorkListItem{{ID: "archived", State: model.StateArchived}},
	}}
	candidates := pickerCandidates(res, nil, "waiting")
	if len(candidates) != 3 || !candidates[1].Current || candidates[2].Item.State != model.StateBacklog || candidates[2].Section != "BACKLOG" {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestPickerBindingsSelectCurrentAndResetFilteredCursor(t *testing.T) {
	candidates := []pickerCandidate{
		{Item: app.WorkListItem{ID: "one"}},
		{Item: app.WorkListItem{ID: "two"}, Current: true},
		{Item: app.WorkListItem{ID: "three"}},
	}
	if got := pickerBindings(candidates); got != "change:first,load:pos(2)" {
		t.Fatalf("bindings = %q", got)
	}
	if got := pickerBindings(candidates[:1]); got != "change:first" {
		t.Fatalf("bindings without current item = %q", got)
	}
}

func TestPickerRowsUseStyledFixedWidthColumns(t *testing.T) {
	candidate := pickerCandidate{Section: "NEEDS ATTENTION", Current: true, Item: app.WorkListItem{Slug: "a-very-long-item-name", Repository: "owner/repository", Title: "Readable title"}}
	row := renderPickerCandidate(candidate, 12, 18)
	if !strings.Contains(row, pickerMagenta+"● ") || !strings.Contains(row, pickerYellow+"ATTENTION") || !strings.Contains(row, "a-very-long…") || !strings.Contains(row, pickerDim+"owner/repository") {
		t.Fatalf("styled row = %q", row)
	}
	if badge := pickerSectionBadge("BACKLOG"); !strings.Contains(badge, pickerCyan+"BACKLOG") {
		t.Fatalf("backlog badge = %q", badge)
	}
	header := renderPickerHeader(12, 18)
	if !strings.Contains(header, "FROZEN SNAPSHOT") || !strings.Contains(header, "STATUS") || !strings.Contains(header, "ITEM") || !strings.Contains(header, "REPOSITORY") {
		t.Fatalf("header = %q", header)
	}
}

func TestPickerTextRemovesTerminalControls(t *testing.T) {
	if got := pickerText("safe\x1b]8;;bad\a\nname"); strings.ContainsAny(got, "\x1b\a\n") {
		t.Fatalf("picker text retained controls: %q", got)
	}
}

func TestPickerPreviewCombinesDurableDetailsAndObservedStatus(t *testing.T) {
	preview := pickerPreviewCommand("/tmp/wi executable")
	if !strings.Contains(preview, " show --item {1}") || !strings.Contains(preview, " agent status --item {1}") || !strings.Contains(preview, "observed status") || !strings.Contains(preview, "'/tmp/wi executable'") {
		t.Fatalf("preview command = %q", preview)
	}
}

func TestSelectWithFZFRejectsUnknownID(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-fzf")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'unknown\\trow\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	candidates := []pickerCandidate{{Item: app.WorkListItem{ID: "item-1", Slug: "one", State: model.StateWorking}}}
	_, _, err := selectWithFZF(context.Background(), Config{Stderr: &bytes.Buffer{}, Env: map[string]string{"WI_FZF": script}}, candidates, true)
	if err == nil || !strings.Contains(err.Error(), "unknown work item") {
		t.Fatalf("err=%v", err)
	}
}

func TestSelectWithFZFUsesOpaqueIDFromCandidate(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-fzf")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nhead -n 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	candidates := []pickerCandidate{{Item: app.WorkListItem{ID: "item-1", Slug: "one", State: model.StateWorking}}}
	selected, ok, err := selectWithFZF(context.Background(), Config{Stderr: &bytes.Buffer{}, Env: map[string]string{"WI_FZF": script}}, candidates, true)
	if err != nil || !ok || selected.Item.ID != "item-1" {
		t.Fatalf("selected=%+v ok=%v err=%v", selected, ok, err)
	}
}
