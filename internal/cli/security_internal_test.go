package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/app"
	viewapp "github.com/regb/workitem/internal/app/view"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/plugin"
)

func TestPromptDirenvApprovalDefaultsToNo(t *testing.T) {
	for _, input := range []string{"\n", "no\n", "unexpected\n"} {
		var output bytes.Buffer
		approved, err := promptExtensionApproval(bytes.NewBufferString(input), &output, plugin.Approval{ItemID: "item", Path: "/repo/.envrc", Question: "Allow?"})
		if err != nil || approved {
			t.Fatalf("input=%q approved=%v err=%v", input, approved, err)
		}
	}
}

func TestPromptDirenvApprovalAcceptsExplicitYes(t *testing.T) {
	var output bytes.Buffer
	approved, err := promptExtensionApproval(bytes.NewBufferString("yes\n"), &output, plugin.Approval{ItemID: "item", Path: "/repo/.envrc", Question: "Allow?"})
	if err != nil || !approved {
		t.Fatalf("approved=%v err=%v", approved, err)
	}
}

func TestPickerCandidatesIncludeActiveBlockedAndBacklog(t *testing.T) {
	res := app.WorkListResult{Sections: app.WorkListSections{
		Active:  []app.WorkListItem{{ID: "working", State: model.StateActive}},
		Waiting: []app.WorkListItem{{ID: "waiting", State: model.StateActive, Waiting: true}},
		Backlog: []app.WorkListItem{{ID: "backlog", State: model.StateBacklog}},
		Closed:  []app.WorkListItem{{ID: "archived", State: model.StateClosed}},
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
	if got := pickerBindings(candidates); got != "change:untrack-current+first+rebind(result),result:first+unbind(result),load:pos(2)+unbind(load)" {
		t.Fatalf("bindings = %q", got)
	}
	if got := pickerBindings(candidates[:1]); got != "change:untrack-current+first+rebind(result),result:first+unbind(result)" {
		t.Fatalf("bindings without current item = %q", got)
	}
}

func TestPickerRowsUseStyledFixedWidthColumns(t *testing.T) {
	candidate := pickerCandidate{Section: "NEEDS ATTENTION", Current: true, Item: app.WorkListItem{Slug: "a-very-long-item-name", Repository: "owner/repository", Title: "Readable title", Unread: true}}
	read := pickerCandidate{Section: "NEEDS ATTENTION", Item: app.WorkListItem{Repository: "owner/repository", Title: "Already read"}}
	snapshot := pickerSnapshot([]pickerCandidate{candidate, read})
	var output strings.Builder
	if err := snapshot.rows(&output, 80); err != nil {
		t.Fatal(err)
	}
	row := output.String()
	if !strings.Contains(row, pickerMagenta+"● ") || !strings.Contains(row, pickerYellow+"ATTENTION") || !strings.Contains(row, pickerBold+"Readable title") || !strings.Contains(row, pickerDim+"owner/repository") {
		t.Fatalf("styled row = %q", row)
	}
	if strings.Contains(row, pickerBold+"Already read") || !strings.Contains(row, "Already read") {
		t.Fatalf("read title must use normal weight: %q", row)
	}
	if badge := pickerSectionBadge("BACKLOG", 9); !strings.Contains(badge, pickerCyan+"BACKLOG") {
		t.Fatalf("backlog badge = %q", badge)
	}
	header := snapshot.header(80)
	if !strings.Contains(header, "STATUS") || !strings.Contains(header, "ITEM") || !strings.Contains(header, "REPOSITORY") {
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
	candidates := []pickerCandidate{{Item: app.WorkListItem{ID: "item-1", Slug: "one", State: model.StateActive}}}
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
	candidates := []pickerCandidate{{Item: app.WorkListItem{ID: "item-1", Slug: "one", State: model.StateActive}}}
	selected, ok, err := selectWithFZF(context.Background(), Config{Stderr: &bytes.Buffer{}, Env: map[string]string{"WI_FZF": script}}, candidates, true)
	if err != nil || !ok || selected.Item.ID != "item-1" {
		t.Fatalf("selected=%+v ok=%v err=%v", selected, ok, err)
	}
}

func TestListShowsUnreadAsBoldOrTextMarker(t *testing.T) {
	res := app.WorkListResult{Sections: app.WorkListSections{Active: []app.WorkListItem{{ID: "01KZYHGDCECSFS4BJ2SNTQP49V", Slug: "unread-item", State: model.StateActive, Unread: true}, {ID: "01KZYHGDCECSFS4BJ2SNTQP49W", Slug: "read-item", State: model.StateActive}}}}
	var plain, colored bytes.Buffer
	printWorkList(&plain, res, "active", false, nil, listDisplayOptions{})
	printWorkList(&colored, res, "active", true, nil, listDisplayOptions{})
	if !strings.Contains(plain.String(), "unread") {
		t.Fatalf("plain output lacks unread marker:\n%s", plain.String())
	}
	if !strings.Contains(colored.String(), "\x1b[1munread-item") || strings.Contains(colored.String(), "\x1b[1mread-item") {
		t.Fatalf("colored output does not bold only unread titles:\n%q", colored.String())
	}
}

func TestListAndPickerShowWaitingLabelAfterTheName(t *testing.T) {
	waiting := app.WorkListItem{ID: "01KZYHGDCECSFS4BJ2SNTQP49V", Slug: "parked", Title: "Parked work", State: model.StateActive, Waiting: true, Repository: "owner/repo", Blockers: []model.Blocker{{Kind: model.BlockerExternal, Event: "activity on acme/api#42", Label: "#42"}, {Kind: model.BlockerItem, ItemID: "01KZYHGDCECSFS4BJ2SNTQP49W"}}}
	other := app.WorkListItem{ID: "01KZYHGDCECSFS4BJ2SNTQP49W", Slug: "api-schema", Title: "API schema", State: model.StateActive, Repository: "owner/repo"}
	res := app.WorkListResult{Sections: app.WorkListSections{Active: []app.WorkListItem{other}, Waiting: []app.WorkListItem{waiting}}}
	var plain bytes.Buffer
	printWorkList(&plain, res, "active", false, nil, listDisplayOptions{})
	if !strings.Contains(plain.String(), "parked  · #42, on api-schema") || strings.Contains(plain.String(), "WAITING FOR") {
		t.Fatalf("list output:\n%s", plain.String())
	}
	snapshot := pickerSnapshot([]pickerCandidate{{Section: "WAITING", Item: waiting}, {Section: "ACTIVE", Item: other}})
	var rows strings.Builder
	if err := snapshot.rows(&rows, 80); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rows.String(), "Parked work"+pickerYellow+" · #42, on") || !strings.Contains(rows.String(), "waiting #42, on api-schema") {
		t.Fatalf("picker rows = %q", rows.String())
	}
}

func TestBusyWaitingItemShowsInProgress(t *testing.T) {
	busy := app.WorkListItem{ID: "busy", State: model.StateActive, Waiting: true, Agent: &viewapp.AgentStatus{Status: "busy", Bucket: "in_progress"}}
	idle := app.WorkListItem{ID: "idle", State: model.StateActive, Waiting: true, Agent: &viewapp.AgentStatus{Status: "idle", Bucket: "needs_attention"}}
	res := app.WorkListResult{Sections: app.WorkListSections{Waiting: []app.WorkListItem{busy, idle}}}
	sections := map[string][]string{}
	for _, section := range workDisplaySections(res, "active", map[string]string{"busy": "busy", "idle": "idle"}) {
		for _, item := range section.Items {
			sections[section.Name] = append(sections[section.Name], item.ID)
		}
	}
	if strings.Join(sections["IN PROGRESS"], ",") != "busy" || strings.Join(sections["WAITING"], ",") != "idle" {
		t.Fatalf("sections = %v", sections)
	}
}
