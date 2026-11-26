package coordinator

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

func TestManifestCommandCommitsAtomicallyAndIdempotently(t *testing.T) {
	now := time.Now().UTC()
	manifest := model.NewManifest(importTestID, "domain", "Domain command", nil, false, now, model.Repository{}, model.Checkout{})
	db, _ := itemDatabase(t, manifest)
	canonical, err := db.CanonicalManifest(importTestID)
	if err != nil || canonical.Revision != 1 {
		t.Fatalf("canonical=%+v err=%v", canonical, err)
	}
	command := ManifestCommand{ID: "command-labels", ProtocolVersion: ProtocolVersion, Type: CommandLabelsAdd, ItemID: importTestID, ExpectedRevision: &canonical.Revision, Actor: model.OperatorUser, Labels: []string{"backend", "urgent"}, CreatedAt: now.Add(time.Second)}
	result, err := db.ExecuteManifestCommand(command)
	if err != nil || !result.Changed || result.Revision != 3 || len(result.Events) != 2 || !model.HasLabel(result.Manifest.Labels, "backend") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if writes, _ := db.PendingNativeWrites(); len(writes) != 0 {
		t.Fatalf("manifest-only command scheduled a native write: %+v", writes)
	}
	duplicate, err := db.ExecuteManifestCommand(command)
	if err != nil || !duplicate.Duplicate || duplicate.Revision != result.Revision {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	different := command
	different.Type = CommandLabelsRemove
	if _, err := db.ExecuteManifestCommand(different); err == nil {
		t.Fatal("expected reused command id with different input to be rejected")
	}
}

func TestManifestCommandRejectsStaleRevisionAtomically(t *testing.T) {
	db, _ := itemDatabase(t, model.NewManifest(importTestID, "conflict", "Conflict", nil, false, time.Now().UTC(), model.Repository{}, model.Checkout{}))
	stale := uint64(0)
	deep := true
	if _, err := db.ExecuteManifestCommand(ManifestCommand{ID: "stale", ProtocolVersion: ProtocolVersion, Type: CommandDeepWorkSet, ItemID: importTestID, ExpectedRevision: &stale, DeepWork: &deep}); err == nil {
		t.Fatal("expected revision conflict")
	}
	canonical, _ := db.CanonicalManifest(importTestID)
	if canonical.Manifest.DeepWork || canonical.Revision != 1 {
		t.Fatalf("stale command changed canonical state: %+v", canonical)
	}
	if writes, _ := db.PendingNativeWrites(); len(writes) != 0 {
		t.Fatalf("stale command scheduled a native write: %+v", writes)
	}
}

func TestTitleCommandOnlyChangesMetadataAndIsIdempotent(t *testing.T) {
	m := model.NewManifest(importTestID, "conversation", "New conversation", nil, false, time.Now(), model.Repository{RootAtCreation: "/repo", CreatedFromCommit: "original"}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "original-branch"))
	db, _ := itemDatabase(t, m)
	before, _ := db.CanonicalManifest(m.ID)
	cmd := ManifestCommand{ID: "generated-title", ProtocolVersion: ProtocolVersion, Type: CommandTitleSet, ItemID: m.ID, ExpectedRevision: &before.Revision, Title: "Fix authentication tests", Actor: model.OperatorUser, CreatedAt: time.Now().UTC()}
	result, err := db.ExecuteManifestCommand(cmd)
	if err != nil || !result.Changed || result.Manifest.Title != cmd.Title || len(result.Events) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Manifest.Slug != m.Slug || !reflect.DeepEqual(result.Manifest.Checkout, m.Checkout) || !reflect.DeepEqual(result.Manifest.Repository, m.Repository) || result.Manifest.State != m.State {
		t.Fatal("title changed identity, lifecycle, or checkout")
	}
	repeated, err := db.ExecuteManifestCommand(cmd)
	if err != nil || !repeated.Duplicate || repeated.Revision != result.Revision {
		t.Fatal("title replay not idempotent")
	}
	for i, title := range []string{"", "line\nbreak", "escape\x1b", strings.Repeat("x", 513)} {
		revision := result.Revision
		invalid := cmd
		invalid.ID = string(rune('a' + i))
		invalid.Title = title
		invalid.ExpectedRevision = &revision
		if _, err := db.ExecuteManifestCommand(invalid); err == nil {
			t.Fatalf("invalid title accepted: %q", title)
		}
	}
	after, _ := db.CanonicalManifest(m.ID)
	if after.Manifest.Title != cmd.Title || after.Revision != result.Revision {
		t.Fatal("rejected command changed title")
	}
}

func TestNamingPolicyKeepsActiveSlugAndBranchButAllowsTitles(t *testing.T) {
	for _, state := range []string{model.StateBacklog, model.StateActive, model.StateClosed} {
		t.Run(state, func(t *testing.T) {
			m := model.NewManifest(importTestID, "task", "Original", nil, false, time.Now(), model.Repository{}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "wi/task-original"))
			m.State = state
			if state == model.StateClosed {
				m.Slug = ""
			}
			m.Checkout.ClaimGeneration = 1
			db, _ := itemDatabase(t, m)
			before, _ := db.CanonicalManifest(m.ID)
			result, err := db.ExecuteManifestCommand(ManifestCommand{ID: "title", ProtocolVersion: ProtocolVersion, Type: CommandTitleSet, ItemID: m.ID, ExpectedRevision: &before.Revision, Title: "Clarified title", CreatedAt: time.Now()})
			if err != nil || result.Manifest.Title != "Clarified title" || result.Manifest.Slug != m.Slug || !reflect.DeepEqual(result.Manifest.Checkout, m.Checkout) {
				t.Fatalf("title result=%+v err=%v", result, err)
			}
			for i, field := range []string{"slug", "branch"} {
				canonical, _ := db.CanonicalManifest(m.ID)
				changed := canonical.Manifest
				if field == "slug" {
					if state == model.StateClosed {
						continue
					}
					changed.Slug = "new-name"
				} else {
					changed.Checkout.Specification.Branch = "new-branch"
				}
				payload, _ := json.Marshal(changed)
				_, err := db.ExecuteStoreCommand(StoreCommand{ID: fmt.Sprintf("unsafe-%d", i), ProtocolVersion: ProtocolVersion, Operation: StoreManifestSave, ItemID: m.ID, ExpectedRevision: &canonical.Revision, PayloadDigest: StorePayloadDigest(payload)}, payload)
				if err == nil {
					t.Fatalf("%s change bypassed naming policy", field)
				}
				after, _ := db.CanonicalManifest(m.ID)
				if after.Revision != canonical.Revision || !reflect.DeepEqual(after.Manifest, canonical.Manifest) {
					t.Fatal("rejected mutation changed canonical state")
				}
			}
		})
	}
}

func TestStateSetCommandEnforcesCapacity(t *testing.T) {
	now := time.Now().UTC()
	candidate := model.NewManifest(importTestID, "domain", "Domain", nil, true, now, model.Repository{}, model.Checkout{})
	active := testManifest("01KZYHGDCECSFS4BJ2SNTQP49W", "active", "Active", now)
	active.DeepWork = true
	db, _ := itemDatabase(t, candidate, active)
	canonical, _ := db.CanonicalManifest(candidate.ID)
	working := ManifestCommand{ID: "state-working", ProtocolVersion: ProtocolVersion, Type: CommandStateSet, ItemID: candidate.ID, ExpectedRevision: &canonical.Revision, Actor: model.OperatorUser, TargetState: model.StateActive, EventType: "work_item.started", MaxActive: 1, CreatedAt: now.Add(time.Second)}
	if _, err := db.ExecuteManifestCommand(working); err == nil || !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("expected capacity error, got %v", err)
	}
	unchanged, _ := db.CanonicalManifest(candidate.ID)
	if unchanged.Manifest.State != model.StateBacklog || unchanged.Revision != canonical.Revision {
		t.Fatalf("rejected transition changed state: %+v", unchanged)
	}
	working.Force = true
	result, err := db.ExecuteManifestCommand(working)
	if err != nil || result.Manifest.State != model.StateActive || result.Revision != canonical.Revision+2 || len(result.Events) != 2 || result.Events[1].Type != "work_item.started" {
		t.Fatalf("forced result=%+v err=%v", result, err)
	}
}

// Closing keeps the slug aside and frees it; reopening allocates a fresh slug
// from the current title, unique among open items, and never renames the
// branch.
func TestClosingFreesTheSlugAndReopeningAllocatesOne(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	m := model.NewManifest(importTestID, "original", "Original", nil, false, now, model.Repository{}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "original-branch"))
	db, _ := itemDatabase(t, m)
	command := func(id, typ, title, state string, at time.Time) ManifestCommandResult {
		t.Helper()
		result, err := runCommand(t, db, ManifestCommand{ID: id, Type: typ, ItemID: m.ID, Title: title, TargetState: state, CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	closed := command("close", CommandStateSet, "", model.StateClosed, now)
	if closed.Manifest.Slug != "" || closed.Manifest.ClosedSlug != "original" {
		t.Fatalf("closed: slug=%q closed_slug=%q", closed.Manifest.Slug, closed.Manifest.ClosedSlug)
	}
	command("title", CommandTitleSet, "Different title", "", now)
	// Another item takes the slug the new title derives.
	holder := model.NewManifest("01KZYHGDCECSFS4BJ2SNTQP49X", "different-title", "Holder", nil, false, now, model.Repository{}, model.Checkout{})
	if _, err := db.ExecuteCreateItemCommand(CreateItemCommand{ID: "holder-create", ProtocolVersion: ProtocolVersion, Manifest: holder, DescriptionDigest: DescriptionDigest(""), Actor: model.OperatorUser, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	restoredAt := now.Add(4 * time.Second)
	restored := command("restore", CommandStateSet, "", model.StateBacklog, restoredAt)
	if restored.Manifest.ID != m.ID || restored.Manifest.Slug != "different-title-2" || restored.Manifest.ClosedSlug != "" || restored.Manifest.Checkout.Specification.Branch != m.Checkout.Specification.Branch {
		t.Fatalf("restore=%+v", restored.Manifest)
	}
	if !restored.Manifest.StateChangedAt.Equal(restoredAt) {
		t.Fatalf("state_changed_at=%s", restored.Manifest.StateChangedAt)
	}
	canonical, _ := db.CanonicalManifest(m.ID)
	if _, err := db.ExecuteManifestCommand(ManifestCommand{ID: "obsolete", ProtocolVersion: ProtocolVersion, Type: "item.naming.finalize", ItemID: m.ID, ExpectedRevision: &canonical.Revision, Title: "Do not rename", CreatedAt: time.Now()}); err == nil {
		t.Fatal("obsolete naming command accepted")
	}
}

func TestWaitCommandComputesAndStoresResultingQueueAtomically(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	ids := []string{importTestID, "01KZYHGDCECSFS4BJ2SNTQP49W", "01KZYHGDCECSFS4BJ2SNTQP49X"}
	var manifests []model.Manifest
	for index, id := range ids {
		manifests = append(manifests, testManifest(id, "queue-"+string(rune('a'+index)), "Queue", now.Add(time.Duration(index)*time.Second)))
	}
	db, _ := itemDatabase(t, manifests...)
	for index, id := range ids {
		requestedAt := now.Add(time.Duration(index) * time.Minute)
		observation := AgentObservation{WorkItemID: id, Status: "idle", Activity: AttentionActivity{WorkItemID: id, LastRequestedAt: &requestedAt}, ObservedAt: now}
		encoded, _ := json.Marshal(observation)
		if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AgentObservationProjection, Key: id, Value: encoded}}); err != nil {
			t.Fatal(err)
		}
	}
	canonical, err := db.CanonicalManifest(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	command := ManifestCommand{ID: "wait-and-queue", ProtocolVersion: ProtocolVersion, Type: CommandBlockerAdd, ItemID: ids[0], ExpectedRevision: &canonical.Revision, Actor: model.OperatorUser, Blockers: []model.Blocker{{Kind: model.BlockerExternal, Event: "review"}}, CreatedAt: now.Add(5 * time.Minute)}
	result, err := db.ExecuteManifestCommandWithQueue(command, ActionabilityQueueOptions{})
	if err != nil || result.Command.Manifest.State != model.StateActive || len(result.Command.Manifest.Blockers) != 1 || result.Queue.Strategy != "recent-request" || len(result.Queue.Candidates) != 2 || result.Queue.Candidates[0].Manifest.ID != ids[2] || result.Queue.Candidates[1].Manifest.ID != ids[1] || !result.Selection.Found || result.Selection.WorkItemID != ids[2] || result.Selection.CurrentInQueue {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	// A retry returns the queue committed with the transition, even if the live
	// observation changes after the original acknowledgement.
	var changed AgentObservation
	if _, err := db.ReadProjection(AgentObservationProjection, ids[2], &changed); err != nil {
		t.Fatal(err)
	}
	changed.Status = "busy"
	encoded, _ := json.Marshal(changed)
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: AgentObservationProjection, Key: ids[2], Value: encoded}}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := db.ExecuteManifestCommandWithQueue(command, ActionabilityQueueOptions{})
	if err != nil || !duplicate.Command.Duplicate || len(duplicate.Queue.Candidates) != 2 || duplicate.Queue.Candidates[0].Manifest.ID != ids[2] {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	if _, err := db.ActionabilityQueue(ActionabilityQueueOptions{Strategy: "missing"}); err == nil {
		t.Fatal("unknown attention priority strategy was accepted")
	}
}
