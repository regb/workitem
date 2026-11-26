package coordinator

import (
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func eventTypes(t *testing.T, db *Database, itemID string) []string {
	t.Helper()
	events, err := db.ListItemEvents(itemID)
	if err != nil {
		t.Fatal(err)
	}
	types := []string{}
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func TestClearedBlockerArmsItsFollowUpUntilSentOrDropped(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := importTestID
	db, _ := itemDatabase(t, testItems(now, a)...)
	if _, err := runCommand(t, db, ManifestCommand{ID: "wait", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{{Kind: model.BlockerExternal, Event: "review", FollowUp: "Address the review"}, {Kind: model.BlockerExternal, Event: "ci"}}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	canonical, _ := db.CanonicalManifest(a)
	first := canonical.Manifest.Blockers[0]
	if first.FollowUp != "Address the review" || len(canonical.Manifest.FollowUps) != 0 {
		t.Fatalf("manifest = %+v", canonical.Manifest)
	}
	cleared := now.Add(time.Minute)
	if _, err := runCommand(t, db, ManifestCommand{ID: "clear", Type: CommandBlockerClear, ItemID: a, BlockerID: first.ID, CreatedAt: cleared}); err != nil {
		t.Fatal(err)
	}
	canonical, _ = db.CanonicalManifest(a)
	want := model.FollowUp{ID: first.ID, Message: "Address the review", BlockedAt: now, ClearedAt: cleared, SetBy: model.OperatorUser}
	if len(canonical.Manifest.FollowUps) != 1 || canonical.Manifest.FollowUps[0] != want {
		t.Fatalf("follow-ups = %+v", canonical.Manifest.FollowUps)
	}
	if payload := clearedCauses(t, db, a)[first.ID]; payload["follow_up"] != true || payload["follow_up_dropped"] != nil {
		t.Fatalf("cleared event = %+v", payload)
	}
	if _, err := runCommand(t, db, ManifestCommand{ID: "done", Type: CommandFollowUpDone, ItemID: a, FollowUpID: first.ID, FollowUpOutcome: FollowUpSent, CreatedAt: cleared}); err != nil {
		t.Fatal(err)
	}
	canonical, _ = db.CanonicalManifest(a)
	if len(canonical.Manifest.FollowUps) != 0 {
		t.Fatalf("sent follow-up stayed: %+v", canonical.Manifest.FollowUps)
	}
	// Reporting it again changes nothing.
	if result, err := runCommand(t, db, ManifestCommand{ID: "done-again", Type: CommandFollowUpDone, ItemID: a, FollowUpID: first.ID, FollowUpOutcome: FollowUpSent, CreatedAt: cleared}); err != nil || result.Changed {
		t.Fatalf("repeat: %+v %v", result, err)
	}
	if _, err := runCommand(t, db, ManifestCommand{ID: "bad", Type: CommandFollowUpDone, ItemID: a, FollowUpID: first.ID, FollowUpOutcome: "lost", CreatedAt: cleared}); err == nil {
		t.Fatal("an unknown outcome was accepted")
	}
	if types := strings.Join(eventTypes(t, db, a), ","); !strings.Contains(types, "blocker.cleared,follow_up.sent") {
		t.Fatalf("events = %s", types)
	}
}

func TestFollowUpIsDroppedWhenOperatorDropsPromptsOrCloses(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, b, c := importTestID, "01KZYHGDCECSFS4BJ2SNTQP49W", "01KZYHGDCECSFS4BJ2SNTQP49X"
	db, _ := itemDatabase(t, testItems(now, a, b, c)...)
	until := now.Add(time.Hour)
	for _, id := range []string{a, b, c} {
		if _, err := runCommand(t, db, ManifestCommand{ID: "wait-" + id, Type: CommandBlockerAdd, ItemID: id, Blockers: []model.Blocker{{Kind: model.BlockerTime, Until: &until, FollowUp: "go"}}, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	// a: cleared by hand without its follow-up.
	if _, err := runCommand(t, db, ManifestCommand{ID: "drop", Type: CommandBlockerClear, ItemID: a, DropFollowUp: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// b: the operator prompted it, which ends the snooze.
	prompted := now.Add(time.Minute)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AttentionActivityProjection, Key: b, Value: mustJSON(t, AttentionActivity{WorkItemID: b, LastRequestedAt: &prompted})}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SweepBlockers(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	// c: closed while waiting.
	if _, err := runCommand(t, db, ManifestCommand{ID: "close", Type: CommandStateSet, ItemID: c, TargetState: model.StateClosed, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b, c} {
		canonical, _ := db.CanonicalManifest(id)
		if len(canonical.Manifest.Blockers) != 0 || len(canonical.Manifest.FollowUps) != 0 {
			t.Fatalf("%s: %+v", id, canonical.Manifest)
		}
		for _, payload := range clearedCauses(t, db, id) {
			if payload["follow_up_dropped"] != true {
				t.Fatalf("%s cleared event = %+v", id, payload)
			}
		}
	}
	// An expired snooze sends it.
	if _, err := runCommand(t, db, ManifestCommand{ID: "reopen", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{{Kind: model.BlockerTime, Until: &until, FollowUp: "go"}}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SweepBlockers(until.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	canonical, _ := db.CanonicalManifest(a)
	if len(canonical.Manifest.FollowUps) != 1 || canonical.Manifest.FollowUps[0].Message != "go" {
		t.Fatalf("expired snooze follow-ups = %+v", canonical.Manifest.FollowUps)
	}
}

func TestUsageBlockerIsSingleAndClearedForUsageOrByPrompt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a := importTestID
	db, _ := itemDatabase(t, testItems(now, a)...)
	reset := now.Add(time.Hour)
	usage := model.Blocker{Kind: model.BlockerUsage, Provider: "claude", Until: &reset, FollowUp: "Continue where you left off."}
	if _, err := runCommand(t, db, ManifestCommand{ID: "usage-1", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{usage, {Kind: model.BlockerExternal, Event: "review"}}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if result, err := runCommand(t, db, ManifestCommand{ID: "usage-2", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{usage}, CreatedAt: now}); err != nil || result.Changed {
		t.Fatalf("second usage blocker: %+v %v", result, err)
	}
	canonical, _ := db.CanonicalManifest(a)
	blockers := canonical.Manifest.Blockers
	if len(blockers) != 2 || !canonical.Manifest.Waiting(reset.Add(time.Hour)) {
		t.Fatalf("a usage blocker stays open past its reset until cleared: %+v", blockers)
	}
	if _, err := db.SweepBlockers(reset.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if canonical, _ = db.CanonicalManifest(a); len(canonical.Manifest.Blockers) != 2 {
		t.Fatalf("the sweep cleared a usage blocker at its reset: %+v", canonical.Manifest.Blockers)
	}
	if _, err := runCommand(t, db, ManifestCommand{ID: "wrong", Type: CommandBlockerClear, ItemID: a, BlockerID: blockers[1].ID, ClearCause: BlockerClearedUsageAvailable, CreatedAt: now}); err == nil {
		t.Fatal("an external blocker was cleared for usage")
	}
	if _, err := runCommand(t, db, ManifestCommand{ID: "all", Type: CommandBlockerClear, ItemID: a, ClearCause: BlockerClearedUsageAvailable, CreatedAt: now}); err == nil {
		t.Fatal("clearing every blocker for usage was accepted")
	}
	if _, err := runCommand(t, db, ManifestCommand{ID: "back", Type: CommandBlockerClear, ItemID: a, BlockerID: blockers[0].ID, ClearCause: BlockerClearedUsageAvailable, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	canonical, _ = db.CanonicalManifest(a)
	if len(canonical.Manifest.FollowUps) != 1 || !canonical.Manifest.Waiting(now) {
		t.Fatalf("the follow-up waits for the other blocker: %+v", canonical.Manifest)
	}
	if payload := clearedCauses(t, db, a)[blockers[0].ID]; payload["cause"] != BlockerClearedUsageAvailable || payload["provider"] != "claude" {
		t.Fatalf("cleared event = %+v", payload)
	}

	// A prompt ends a usage blocker and drops its follow-up.
	if _, err := runCommand(t, db, ManifestCommand{ID: "usage-3", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{usage}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	canonical, _ = db.CanonicalManifest(a)
	third := canonical.Manifest.Blockers[1]
	prompted := now.Add(time.Minute)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AttentionActivityProjection, Key: a, Value: mustJSON(t, AttentionActivity{WorkItemID: a, LastRequestedAt: &prompted})}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SweepBlockers(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	canonical, _ = db.CanonicalManifest(a)
	if len(canonical.Manifest.Blockers) != 1 || len(canonical.Manifest.FollowUps) != 1 {
		t.Fatalf("after prompt: %+v", canonical.Manifest)
	}
	if payload := clearedCauses(t, db, a)[third.ID]; payload["cause"] != BlockerClearedPrompted || payload["follow_up_dropped"] != true {
		t.Fatalf("prompted usage blocker = %+v", payload)
	}
}
