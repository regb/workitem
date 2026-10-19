package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/app"
	"github.com/regb/workitem/internal/model"
	"github.com/rivo/uniseg"
)

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
