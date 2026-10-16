package coordinator

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

func archiveTestCommand(id string, previous, archived bool) SetRepositoryArchivedCommand {
	return SetRepositoryArchivedCommand{ID: "archive:" + id, ProtocolVersion: ProtocolVersion, RepositoryID: importTestID, PreviousArchived: previous, Archived: archived, CreatedAt: time.Now().UTC()}
}

func TestRepositoryArchiveRestoreDurableAndIdempotent(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registered, err := db.RegisterRepository(repositoryTestCommand(importTestID, "project"))
	if err != nil {
		t.Fatal(err)
	}
	command := archiveTestCommand("first", false, true)
	archived, err := db.SetRepositoryArchived(command)
	if err != nil {
		t.Fatal(err)
	}
	expected := registered.Repository
	expected.Archived = true
	if !archived.Changed || !reflect.DeepEqual(expected, archived.Repository) || archived.Sequence != registered.Sequence+1 {
		t.Fatalf("archive=%+v", archived)
	}
	duplicate, err := db.SetRepositoryArchived(command)
	if err != nil || !duplicate.Duplicate || duplicate.Sequence != archived.Sequence {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	noop, err := db.SetRepositoryArchived(archiveTestCommand("noop", true, true))
	if err != nil || noop.Changed {
		t.Fatalf("noop=%+v err=%v", noop, err)
	}
	if status, _ := db.Status(); status.GlobalSequence != archived.Sequence {
		t.Fatal("no-op appended an event")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	repos, err := reopened.ListRepositories()
	if err != nil || len(repos) != 1 || !reflect.DeepEqual(repos[0], expected) {
		t.Fatalf("repos=%+v err=%v", repos, err)
	}
	otherID, _ := model.NewID()
	if _, err := reopened.RegisterRepository(repositoryTestCommand(otherID, "project")); err == nil {
		t.Fatal("archived name reused")
	}
	restored, err := reopened.SetRepositoryArchived(archiveTestCommand("restore", true, false))
	if err != nil || !restored.Changed || !reflect.DeepEqual(restored.Repository, registered.Repository) {
		t.Fatalf("restore=%+v err=%v", restored, err)
	}
	if err := reopened.db.View(func(tx *bolt.Tx) error {
		for _, test := range []struct {
			sequence uint64
			kind     string
		}{{archived.Sequence, "repository.archived"}, {restored.Sequence, "repository.restored"}} {
			var event DomainEvent
			if err := json.Unmarshal(tx.Bucket(bucketEvents).Get(encodeUint64(test.sequence)), &event); err != nil {
				return err
			}
			if event.Type != test.kind {
				t.Fatalf("event=%+v", event)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryArchiveFencesCreationButNotExistingItems(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.RegisterRepository(repositoryTestCommand(importTestID, "project")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	makeCreate := func(id string) CreateItemCommand {
		m := model.NewManifest(id, model.IDDerivedSlug(id), "Task", nil, false, now, model.Repository{ID: importTestID, CreatedFromCommit: strings.Repeat("a", 40)}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "wi/task-"+id))
		return CreateItemCommand{ID: "create:" + id, ProtocolVersion: ProtocolVersion, Manifest: m, DescriptionDigest: DescriptionDigest(""), Actor: "user", CreatedAt: now}
	}
	firstID, _ := model.NewID()
	first := makeCreate(firstID)
	created, err := db.ExecuteCreateItemCommand(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetRepositoryArchived(archiveTestCommand("archive", false, true)); err != nil {
		t.Fatal(err)
	}
	replay, err := db.ExecuteCreateItemCommand(first)
	if err != nil || !replay.Duplicate {
		t.Fatalf("committed replay=%+v err=%v", replay, err)
	}
	secondID, _ := model.NewID()
	second := makeCreate(secondID)
	if _, err := db.ExecuteCreateItemCommand(second); err == nil || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("new item err=%v", err)
	}
	second.Actor = "agent"
	if _, err := db.ExecuteAgentCreateItemCommand(second); err == nil || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("agent creation err=%v", err)
	}
	transition := ManifestCommand{ID: "existing:start", ProtocolVersion: ProtocolVersion, Type: CommandStateSet, ItemID: firstID, ExpectedRevision: &created.Revision, Actor: "user", TargetState: model.StateActive, CreatedAt: now}
	working, err := db.ExecuteManifestCommand(transition)
	if err != nil || working.Manifest.State != model.StateActive {
		t.Fatalf("existing transition=%+v err=%v", working, err)
	}
	// Full manifest saves must also continue working, not just domain commands.
	working.Manifest.Title = "Still editable"
	payload, _ := json.Marshal(working.Manifest)
	storeCommand := StoreCommand{ID: "existing:save", ProtocolVersion: ProtocolVersion, Operation: StoreManifestSave, ItemID: firstID, ExpectedRevision: &working.Revision, PayloadDigest: StorePayloadDigest(payload), CreatedAt: now}
	if _, err := db.ExecuteStoreCommand(storeCommand, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetRepositoryArchived(archiveTestCommand("restore", true, false)); err != nil {
		t.Fatal(err)
	}
	second.Actor = "user"
	if _, err := db.ExecuteCreateItemCommand(second); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryArchiveRejectsMalformedConflictingAndStaleCommands(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registration := repositoryTestCommand(importTestID, "project")
	if _, err := db.RegisterRepository(registration); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SetRepositoryArchivedCommand){
		func(c *SetRepositoryArchivedCommand) { c.RepositoryID = "../escape" },
		func(c *SetRepositoryArchivedCommand) { c.RepositoryID = "01AAAAAAAAAAAAAAAAAAAAAAAA" },
		func(c *SetRepositoryArchivedCommand) { c.ProtocolVersion++ },
		func(c *SetRepositoryArchivedCommand) { c.CreatedAt = time.Time{} },
		func(c *SetRepositoryArchivedCommand) { c.ID = registration.ID },
		func(c *SetRepositoryArchivedCommand) { c.ID = "" },
		func(c *SetRepositoryArchivedCommand) { c.PreviousArchived = true },
	} {
		c := archiveTestCommand("invalid", false, true)
		change(&c)
		if _, err := db.SetRepositoryArchived(c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	c := archiveTestCommand("valid", false, true)
	if _, err := db.SetRepositoryArchived(c); err != nil {
		t.Fatal(err)
	}
	c.Archived = false
	if _, err := db.SetRepositoryArchived(c); err == nil {
		t.Fatal("accepted changed command input")
	}
	stale := archiveTestCommand("stale", false, false)
	if _, err := db.SetRepositoryArchived(stale); err == nil {
		t.Fatal("accepted stale archive state")
	}
	if agentMethodAllowed(MethodSetRepositoryArchived) {
		t.Fatal("agent endpoint permits archive/restore")
	}
}

func TestConcurrentRepositoryArchivesHaveOneWinner(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.RegisterRepository(repositoryTestCommand(importTestID, "project")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		command := archiveTestCommand(id, false, true)
		wg.Add(1)
		go func() { defer wg.Done(); _, err := db.SetRepositoryArchived(command); results <- err }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successes=%d", successes)
	}
}
