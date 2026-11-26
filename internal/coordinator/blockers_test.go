package coordinator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func runCommand(t *testing.T, db *Database, command ManifestCommand) (ManifestCommandResult, error) {
	t.Helper()
	canonical, err := db.CanonicalManifest(command.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	command.ProtocolVersion, command.ExpectedRevision, command.Actor = ProtocolVersion, &canonical.Revision, model.OperatorUser
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
	db, _ := itemDatabase(t, testItems(now, a, b)...)

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
	db, _ := itemDatabase(t, testItems(now, importTestID)...)
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
	db, _ := itemDatabase(t, testItems(now, a, b)...)
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

func TestWaitAddsSeveralBlockersAtomicallyWithMetadata(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, b := importTestID, "01KZYHGDCECSFS4BJ2SNTQP49W"
	db, _ := itemDatabase(t, testItems(now, a, b)...)
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

func TestPromptEndsSnoozeButNotItemOrEventWaits(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	a, b := importTestID, "01KZYHGDCECSFS4BJ2SNTQP49W"
	db, _ := itemDatabase(t, testItems(now, a, b)...)
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
