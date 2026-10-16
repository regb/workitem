package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	bolt "go.etcd.io/bbolt"
)

func blockerTestDB(t *testing.T, now time.Time, ids ...string) *Database {
	t.Helper()
	root := t.TempDir()
	native := store.New(root)
	if err := native.Ensure(); err != nil {
		t.Fatal(err)
	}
	for index, id := range ids {
		manifest := model.NewManifest(id, "blocker-"+string(rune('a'+index)), "Blocker", nil, false, now, model.Repository{}, model.Checkout{})
		manifest.State = model.StateActive
		if err := native.CreateItem(context.Background(), manifest); err != nil {
			t.Fatal(err)
		}
	}
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := seedManifests(context.Background(), db, root); err != nil {
		t.Fatal(err)
	}
	return db
}

func runCommand(t *testing.T, db *Database, command ManifestCommand) (ManifestCommandResult, error) {
	t.Helper()
	canonical, err := db.CanonicalManifest(command.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	command.ProtocolVersion, command.ExpectedRevision, command.Actor = ProtocolVersion, &canonical.Revision, "user"
	return db.ExecuteManifestCommand(command)
}

func clearedCauses(t *testing.T, db *Database, itemID string) map[string]map[string]any {
	t.Helper()
	events, err := db.ListItemEvents(itemID)
	if err != nil {
		t.Fatal(err)
	}
	causes := map[string]map[string]any{}
	for _, event := range events {
		if event.Type != "blocker.cleared" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		causes[payload["blocker_id"].(string)] = payload
	}
	return causes
}

func TestBlockersAreTypedClearedByConditionAndRefuseCycles(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, b := importTestID, "01KZYHGDCECSFS4BJ2SNTQP49W"
	db := blockerTestDB(t, now, a, b)

	onB, err := runCommand(t, db, ManifestCommand{ID: "a-on-b", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{{Kind: model.BlockerItem, ItemID: b}}, CreatedAt: now})
	if err != nil || len(onB.Manifest.Blockers) != 1 || !onB.Manifest.Waiting(now) {
		t.Fatalf("item blocker=%+v err=%v", onB, err)
	}
	if _, err := runCommand(t, db, ManifestCommand{ID: "b-on-a", Type: CommandBlockerAdd, ItemID: b, Blockers: []model.Blocker{{Kind: model.BlockerItem, ItemID: a}}, CreatedAt: now}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("dependency cycle accepted: %v", err)
	}
	until := now.Add(time.Hour)
	if _, err := runCommand(t, db, ManifestCommand{ID: "a-snooze", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{{Kind: model.BlockerTime, Until: &until}}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	external, err := runCommand(t, db, ManifestCommand{ID: "a-review", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{{Kind: model.BlockerExternal, Event: "PR review"}}, CreatedAt: now})
	if err != nil || len(external.Manifest.Blockers) != 3 {
		t.Fatalf("external=%+v err=%v", external, err)
	}
	if _, err := runCommand(t, db, ManifestCommand{ID: "a-no-reason", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{{Kind: model.BlockerExternal}}, CreatedAt: now}); err == nil {
		t.Fatal("external blocker without a reason was accepted")
	}

	// An expired time blocker stops counting immediately; the sweep records it.
	later := now.Add(2 * time.Hour)
	canonical, _ := db.CanonicalManifest(a)
	if len(canonical.Manifest.OpenBlockers(later)) != 2 {
		t.Fatalf("open blockers after expiry = %+v", canonical.Manifest.OpenBlockers(later))
	}
	if cleared, err := db.SweepBlockers(later); err != nil || cleared != 1 {
		t.Fatalf("sweep cleared=%d err=%v", cleared, err)
	}
	canonical, _ = db.CanonicalManifest(a)
	if len(canonical.Manifest.Blockers) != 2 {
		t.Fatalf("blockers after expiry sweep = %+v", canonical.Manifest.Blockers)
	}

	closed, err := runCommand(t, db, ManifestCommand{ID: "b-close", Type: CommandStateSet, ItemID: b, TargetState: model.StateClosed, Resolution: model.ResolutionAbandoned, CreatedAt: later})
	if err != nil || closed.Manifest.Resolution != model.ResolutionAbandoned {
		t.Fatalf("close=%+v err=%v", closed, err)
	}
	if cleared, err := db.SweepBlockers(later); err != nil || cleared != 1 {
		t.Fatalf("item sweep cleared=%d err=%v", cleared, err)
	}
	causes := clearedCauses(t, db, a)
	itemBlocker := onB.Manifest.Blockers[0].ID
	if causes[itemBlocker]["cause"] != BlockerClearedItemClosed || causes[itemBlocker]["blocking_item_resolution"] != model.ResolutionAbandoned {
		t.Fatalf("item blocker clearance = %+v", causes[itemBlocker])
	}
	expired := 0
	for _, payload := range causes {
		if payload["cause"] == BlockerClearedExpired {
			expired++
		}
	}
	if expired != 1 {
		t.Fatalf("expired clearances = %+v", causes)
	}

	// External blockers are cleared only explicitly.
	canonical, _ = db.CanonicalManifest(a)
	if len(canonical.Manifest.Blockers) != 1 || canonical.Manifest.Blockers[0].Kind != model.BlockerExternal {
		t.Fatalf("remaining blockers = %+v", canonical.Manifest.Blockers)
	}
	cleared, err := runCommand(t, db, ManifestCommand{ID: "a-unblock", Type: CommandBlockerClear, ItemID: a, BlockerID: canonical.Manifest.Blockers[0].ID, CreatedAt: later})
	if err != nil || len(cleared.Manifest.Blockers) != 0 {
		t.Fatalf("manual clear=%+v err=%v", cleared, err)
	}

	reopened, err := runCommand(t, db, ManifestCommand{ID: "b-reopen", Type: CommandStateSet, ItemID: b, TargetState: model.StateBacklog, CreatedAt: later})
	if err != nil || reopened.Manifest.Resolution != "" || reopened.Manifest.Slug == "" {
		t.Fatalf("reopen=%+v err=%v", reopened, err)
	}
	if _, err := runCommand(t, db, ManifestCommand{ID: "b-waiting", Type: CommandStateSet, ItemID: b, TargetState: model.LegacyStateWaiting, CreatedAt: later}); err == nil {
		t.Fatal("waiting state accepted")
	}
}

func TestClosingClearsOwnBlockers(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	db := blockerTestDB(t, now, importTestID)
	if _, err := runCommand(t, db, ManifestCommand{ID: "review", Type: CommandBlockerAdd, ItemID: importTestID, Blockers: []model.Blocker{{Kind: model.BlockerExternal, Event: "review"}}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	closed, err := runCommand(t, db, ManifestCommand{ID: "close", Type: CommandStateSet, ItemID: importTestID, TargetState: model.StateClosed, CreatedAt: now})
	if err != nil || closed.Manifest.Resolution != model.ResolutionCompleted || len(closed.Manifest.Blockers) != 0 {
		t.Fatalf("close=%+v err=%v", closed, err)
	}
	for _, payload := range clearedCauses(t, db, importTestID) {
		if payload["cause"] != BlockerClearedClosed {
			t.Fatalf("clearance = %+v", payload)
		}
	}
}

func TestBlockedItemsLeaveQueueUntilAResponseIsUnread(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, b := importTestID, "01KZYHGDCECSFS4BJ2SNTQP49W"
	db := blockerTestDB(t, now, a, b)
	completed := now.Add(-time.Minute)
	for _, id := range []string{a, b} {
		encoded, _ := json.Marshal(AgentObservation{WorkItemID: id, Status: "idle", Activity: AttentionActivity{WorkItemID: id, LastRequestedAt: &completed, LastCompletedAt: &completed}, ObservedAt: now})
		if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AgentObservationProjection, Key: id, Value: encoded}}); err != nil {
			t.Fatal(err)
		}
	}
	queued := func() []string {
		t.Helper()
		queue, err := db.ActionabilityQueue(ActionabilityQueueOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, candidate := range queue.Candidates {
			ids = append(ids, candidate.Manifest.ID)
		}
		return ids
	}
	if got := queued(); len(got) != 2 {
		t.Fatalf("initial queue = %v", got)
	}
	// Blocking marks the current response read, so the item leaves the queue.
	if _, err := runCommand(t, db, ManifestCommand{ID: "block", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{{Kind: model.BlockerExternal, Event: "review"}}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got := queued(); len(got) != 1 || got[0] != b {
		t.Fatalf("queue after block = %v", got)
	}
	// A newer response is unread and brings the blocked item back.
	newer := now.Add(time.Minute)
	encoded, _ := json.Marshal(AgentObservation{WorkItemID: a, Status: "idle", Activity: AttentionActivity{WorkItemID: a, LastRequestedAt: &completed, LastCompletedAt: &newer}, ObservedAt: now})
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AgentObservationProjection, Key: a, Value: encoded}}); err != nil {
		t.Fatal(err)
	}
	queue, err := db.ActionabilityQueue(ActionabilityQueueOptions{})
	if err != nil || len(queue.Candidates) != 2 {
		t.Fatalf("queue with unread blocked item = %+v err=%v", queue, err)
	}
	// Reading it returns it to review.
	read := newer.Add(time.Second)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AttentionActivityProjection, Key: a, Value: mustJSON(t, AttentionActivity{WorkItemID: a, LastReadAt: &read})}}); err != nil {
		t.Fatal(err)
	}
	if got := queued(); len(got) != 1 || got[0] != b {
		t.Fatalf("queue after read = %v", got)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestSchemaFiveUpgradeReplacesWaitingWithBlockerAndKeepsBackup(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	legacy := map[string]string{importTestID: "working", "01KZYHGDCECSFS4BJ2SNTQP49W": "waiting", "01KZYHGDCECSFS4BJ2SNTQP49X": "archived", "01KZYHGDCECSFS4BJ2SNTQP49Y": "backlog"}
	if err := db.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection))
		for id, state := range legacy {
			manifest := model.NewManifest(id, "legacy-"+state, "Legacy", nil, false, now, model.Repository{}, model.Checkout{})
			if state == "archived" {
				manifest.Slug = ""
			}
			encoded, _ := json.Marshal(manifest)
			var fields map[string]any
			_ = json.Unmarshal(encoded, &fields)
			fields["state"] = state
			encoded, _ = json.Marshal(fields)
			if err := bucket.Put([]byte(id), encoded); err != nil {
				return err
			}
		}
		return tx.Bucket(bucketMeta).Put(keySchemaVersion, encodeUint64(5))
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	upgraded, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if _, err := os.Stat(DatabasePath(root) + ".schema-5.bak"); err != nil {
		t.Fatalf("pre-upgrade backup missing: %v", err)
	}
	want := map[string]string{importTestID: model.StateActive, "01KZYHGDCECSFS4BJ2SNTQP49W": model.StateActive, "01KZYHGDCECSFS4BJ2SNTQP49X": model.StateClosed, "01KZYHGDCECSFS4BJ2SNTQP49Y": model.StateBacklog}
	for id, state := range want {
		canonical, err := upgraded.CanonicalManifest(id)
		if err != nil || canonical.Manifest.State != state {
			t.Fatalf("%s state=%q err=%v", id, canonical.Manifest.State, err)
		}
		if _, err := store.NormalizeManifest(canonical.Manifest); err != nil {
			t.Fatalf("%s invalid after upgrade: %v", id, err)
		}
		if state == model.StateClosed {
			continue
		}
		var activity AttentionActivity
		if found, err := upgraded.ReadProjection(AttentionActivityProjection, id, &activity); err != nil || !found || activity.LastReadAt == nil {
			t.Fatalf("%s activity=%+v found=%v err=%v", id, activity, found, err)
		}
		events, err := upgraded.ListItemEvents(id)
		if err != nil || len(events) == 0 || events[len(events)-1].Type != "attention.read" {
			t.Fatalf("%s upgrade events=%+v err=%v", id, events, err)
		}
	}
	waiting, _ := upgraded.CanonicalManifest("01KZYHGDCECSFS4BJ2SNTQP49W")
	if len(waiting.Manifest.Blockers) != 1 || waiting.Manifest.Blockers[0].ID != MigratedWaitingBlockerID || waiting.Manifest.Blockers[0].Kind != model.BlockerExternal {
		t.Fatalf("waiting item blockers = %+v", waiting.Manifest.Blockers)
	}
	status, _ := upgraded.Status()
	if status.SchemaVersion != SchemaVersion {
		t.Fatalf("schema = %d", status.SchemaVersion)
	}
}

func TestWaitAddsSeveralBlockersAtomicallyWithMetadata(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, b := importTestID, "01KZYHGDCECSFS4BJ2SNTQP49W"
	db := blockerTestDB(t, now, a, b)
	until := now.Add(time.Hour)
	meta := map[string]string{"source": "github", "pr": "https://github.com/acme/api/pull/42"}
	if _, err := runCommand(t, db, ManifestCommand{ID: "invalid-pair", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{{Kind: model.BlockerTime, Until: &until}, {Kind: model.BlockerItem, ItemID: a}}, CreatedAt: now}); err == nil {
		t.Fatal("self dependency accepted")
	}
	if canonical, _ := db.CanonicalManifest(a); len(canonical.Manifest.Blockers) != 0 {
		t.Fatalf("partial add retained: %+v", canonical.Manifest.Blockers)
	}
	added, err := runCommand(t, db, ManifestCommand{ID: "pair", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{{Kind: model.BlockerTime, Until: &until, Meta: meta}, {Kind: model.BlockerItem, ItemID: b, Meta: meta}}, CreatedAt: now})
	if err != nil || len(added.Manifest.Blockers) != 2 || added.Manifest.Blockers[0].ID == added.Manifest.Blockers[1].ID || added.Manifest.Blockers[1].Meta["pr"] != meta["pr"] {
		t.Fatalf("pair=%+v err=%v", added, err)
	}
	meta["pr"] = "mutated"
	if canonical, _ := db.CanonicalManifest(a); canonical.Manifest.Blockers[0].Meta["pr"] == "mutated" {
		t.Fatal("stored metadata aliases the request map")
	}
	events, err := db.ListItemEvents(a)
	if err != nil {
		t.Fatal(err)
	}
	withMeta := 0
	for _, event := range events {
		var payload map[string]any
		_ = json.Unmarshal(event.Payload, &payload)
		if event.Type == "blocker.added" && payload["meta"] != nil {
			withMeta++
		}
	}
	if withMeta != 2 {
		t.Fatalf("blocker.added events with metadata = %d", withMeta)
	}
	if _, err := runCommand(t, db, ManifestCommand{ID: "bad-meta", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{{Kind: model.BlockerExternal, Event: "x", Meta: map[string]string{"Bad Key": "x"}}}, CreatedAt: now}); err == nil {
		t.Fatal("invalid metadata key accepted")
	}
}

func TestSchemaSixUpgradeTurnsReasonsIntoEventNames(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	manifest := model.NewManifest(importTestID, "legacy", "Legacy", nil, false, now, model.Repository{}, model.Checkout{})
	manifest.State = model.StateActive
	encoded, _ := json.Marshal(manifest)
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	until := now.Add(time.Hour)
	fields["blockers"] = []map[string]any{
		{"id": "bwaiting", "kind": "external", "reason": "Waiting (migrated from the former waiting state)", "created_at": now},
		{"id": "b1", "kind": "external", "reason": "API review", "created_at": now},
		{"id": "b2", "kind": "time", "until": until, "reason": "freeze", "created_at": now},
	}
	encoded, _ = json.Marshal(fields)
	if err := db.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection)).Put([]byte(importTestID), encoded); err != nil {
			return err
		}
		return tx.Bucket(bucketMeta).Put(keySchemaVersion, encodeUint64(6))
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	upgraded, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if _, err := os.Stat(DatabasePath(root) + ".schema-6.bak"); err != nil {
		t.Fatalf("pre-upgrade backup missing: %v", err)
	}
	canonical, err := upgraded.CanonicalManifest(importTestID)
	if err != nil {
		t.Fatal(err)
	}
	blockers := canonical.Manifest.Blockers
	if len(blockers) != 3 || blockers[0].Event != MigratedWaitingEvent || blockers[1].Event != "API review" || blockers[2].Event != "" || blockers[2].Until == nil {
		t.Fatalf("blockers = %+v", blockers)
	}
	for _, blocker := range blockers {
		if err := model.ValidateBlocker(blocker, importTestID, now.Add(-time.Minute)); err != nil {
			t.Fatalf("%+v invalid after upgrade: %v", blocker, err)
		}
	}
}

func TestPromptEndsSnoozeButNotItemOrEventWaits(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, b := importTestID, "01KZYHGDCECSFS4BJ2SNTQP49W"
	db := blockerTestDB(t, now, a, b)
	until := now.Add(24 * time.Hour)
	if _, err := runCommand(t, db, ManifestCommand{ID: "snooze", Type: CommandBlockerAdd, ItemID: a, Blockers: []model.Blocker{{Kind: model.BlockerTime, Until: &until}, {Kind: model.BlockerItem, ItemID: b}, {Kind: model.BlockerExternal, Event: "review"}}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	before := now.Add(-time.Minute)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AttentionActivityProjection, Key: a, Value: mustJSON(t, AttentionActivity{WorkItemID: a, LastRequestedAt: &before})}}); err != nil {
		t.Fatal(err)
	}
	if cleared, err := db.SweepBlockers(now.Add(time.Minute)); err != nil || cleared != 0 {
		t.Fatalf("an earlier prompt ended the snooze: cleared=%d err=%v", cleared, err)
	}
	prompted := now.Add(time.Minute)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AttentionActivityProjection, Key: a, Value: mustJSON(t, AttentionActivity{WorkItemID: a, LastRequestedAt: &prompted})}}); err != nil {
		t.Fatal(err)
	}
	if cleared, err := db.SweepBlockers(now.Add(2 * time.Minute)); err != nil || cleared != 1 {
		t.Fatalf("cleared=%d err=%v", cleared, err)
	}
	canonical, _ := db.CanonicalManifest(a)
	if len(canonical.Manifest.Blockers) != 2 || canonical.Manifest.Blockers[0].Kind != model.BlockerItem || canonical.Manifest.Blockers[1].Kind != model.BlockerExternal {
		t.Fatalf("remaining blockers = %+v", canonical.Manifest.Blockers)
	}
	for _, payload := range clearedCauses(t, db, a) {
		if payload["cause"] != BlockerClearedPrompted {
			t.Fatalf("cause = %+v", payload)
		}
	}
}
