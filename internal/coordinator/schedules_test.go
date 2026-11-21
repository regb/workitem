package coordinator

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/regb/workitem/internal/apperr"
	"github.com/regb/workitem/internal/model"
)

func TestSchedulesAreStoredEditedAndAdvancedOnce(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 11, 19, 8, 0, 0, 0, time.Local)
	created, err := db.PutSchedule(model.Schedule{RepositoryID: "r", Cron: "0 9 * * *", Prompt: "p", AgentProfile: "a", Labels: []string{"ci"}, Enabled: true}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	due := now.Add(time.Hour).UTC()
	if created.ID == "" || created.Revision != 1 || created.NextRunAt == nil || !created.NextRunAt.Equal(due) {
		t.Fatalf("created: %+v", created)
	}
	if _, err := db.PutSchedule(model.Schedule{ID: created.ID, RepositoryID: "r", Cron: "0 9 * * *", Prompt: "q", AgentProfile: "a"}, 7, now); !errors.Is(err, ErrScheduleChanged) {
		t.Fatalf("stale edit: %v", err)
	}
	// The first caller takes the firing; a second finds it taken.
	advanced, taken, err := db.AdvanceSchedule(created.ID, due, due.Add(time.Minute))
	if err != nil || !taken || advanced.NextRunAt == nil || !advanced.NextRunAt.Equal(due.AddDate(0, 0, 1)) || advanced.Revision != 1 {
		t.Fatalf("advance: %+v %v %v", advanced, taken, err)
	}
	if _, taken, err := db.AdvanceSchedule(created.ID, due, due.Add(time.Minute)); err != nil || taken {
		t.Fatalf("second advance: %v %v", taken, err)
	}
	if err := db.RecordScheduleRun(created.ID, due, "item", ""); err != nil {
		t.Fatal(err)
	}
	// Disabling clears the next run; an edit keeps the last run.
	edited, err := db.PutSchedule(model.Schedule{ID: created.ID, RepositoryID: "r", Cron: "0 9 * * *", Prompt: "q", AgentProfile: "a"}, 1, now)
	if err != nil || edited.Revision != 2 || edited.NextRunAt != nil || edited.LastItemID != "item" || edited.Prompt != "q" {
		t.Fatalf("edited: %+v %v", edited, err)
	}
	all, err := db.ListSchedules()
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v %v", all, err)
	}
	if err := db.DeleteSchedule(created.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSchedule(created.ID); err == nil {
		t.Fatal("deleted twice")
	}
}

func TestScheduleAndProfileWritesWakeChangeReaders(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before, _ := db.ChangesSnapshot("", 0)
	revision := before.Revision
	advanced := func(what string) {
		t.Helper()
		now, _ := db.ChangesSnapshot(before.Epoch, revision)
		if now.Revision <= revision || !now.All {
			t.Fatalf("%s did not wake change readers: %+v", what, now)
		}
		revision = now.Revision
	}
	created, err := db.PutSchedule(model.Schedule{RepositoryID: "r", Cron: "0 9 * * *", Prompt: "p", AgentProfile: "a", Enabled: true}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	advanced("adding a schedule")
	if err := db.DeleteSchedule(created.ID); err != nil {
		t.Fatal(err)
	}
	advanced("deleting a schedule")
	if _, err := db.ChangeAgentProfile(ChangeAgentProfileCommand{SetTitleModel: true, TitleModel: &model.ModelChoice{Driver: "claude", Model: "haiku"}}); err != nil {
		t.Fatal(err)
	}
	advanced("setting the title model")
}

func TestProtocolErrorsKeepTheirKind(t *testing.T) {
	err := protocolErr("command_error", fmt.Errorf("put: %w", ErrScheduleChanged))
	if err.Kind != "stale" || !errors.Is(err, apperr.Stale) || errors.Is(err, apperr.NotFound) {
		t.Fatalf("%+v", err)
	}
	if plain := protocolErr("command_error", errors.New("plain")); plain.Kind != "" || errors.Is(plain, apperr.Stale) {
		t.Fatalf("%+v", plain)
	}
}
