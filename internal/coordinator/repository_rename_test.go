package coordinator

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

func renameTestCommand(id, previous, name string) RenameRepositoryCommand {
	return RenameRepositoryCommand{ID: "rename:" + id, ProtocolVersion: ProtocolVersion, RepositoryID: importTestID, PreviousName: previous, Name: name, CreatedAt: time.Now().UTC()}
}

func TestRepositoryRenamePreservesIdentityAndIsIdempotent(t *testing.T) {
	db, _ := itemDatabase(t)
	registered, err := db.RegisterRepository(repositoryTestCommand(importTestID, "fleet"))
	if err != nil {
		t.Fatal(err)
	}
	command := renameTestCommand("first", "fleet", "personal-fleet")
	first, err := db.RenameRepository(command)
	if err != nil {
		t.Fatal(err)
	}
	expected := registered.Repository
	expected.Name = command.Name
	if !first.Changed || first.PreviousName != "fleet" || first.Sequence != registered.Sequence+1 || !reflect.DeepEqual(first.Repository, expected) {
		t.Fatalf("rename=%+v", first)
	}
	oldPath, _ := model.OwnedRepositoryGitDir("/data", registered.Repository.ID)
	newPath, _ := model.OwnedRepositoryGitDir("/data", first.Repository.ID)
	if oldPath != newPath {
		t.Fatal("Git store path changed")
	}
	duplicate, err := db.RenameRepository(command)
	if err != nil || !duplicate.Duplicate || duplicate.Sequence != first.Sequence {
		t.Fatalf("retry=%+v, %v", duplicate, err)
	}
	changed := command
	changed.Name = "different"
	if _, err := db.RenameRepository(changed); err == nil {
		t.Fatal("accepted changed command input")
	}
	stale := renameTestCommand("stale", "fleet", "new-name")
	if _, err := db.RenameRepository(stale); err == nil {
		t.Fatal("accepted stale previous name")
	}
	noOp, err := db.RenameRepository(renameTestCommand("noop", "personal-fleet", "personal-fleet"))
	if err != nil || noOp.Changed {
		t.Fatalf("noop=%+v %v", noOp, err)
	}
	status, _ := db.Status()
	if status.GlobalSequence != first.Sequence {
		t.Fatal("retry, rejection or noop appended events")
	}
	repos, err := db.ListRepositories()
	if err != nil || len(repos) != 1 || !reflect.DeepEqual(repos[0], expected) {
		t.Fatalf("registry=%+v %v", repos, err)
	}
	err = db.db.View(func(tx *bolt.Tx) error {
		var event DomainEvent
		if err := json.Unmarshal(tx.Bucket(bucketEvents).Get(encodeUint64(first.Sequence)), &event); err != nil {
			return err
		}
		var payload map[string]string
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		if event.Type != "repository.renamed" || payload["repository_id"] != importTestID || payload["previous_name"] != "fleet" || payload["name"] != expected.Name {
			t.Fatalf("event=%+v payload=%v", event, payload)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryRenameRejectsInvalidAndConflictingCommands(t *testing.T) {
	db, _ := itemDatabase(t)
	registration := repositoryTestCommand(importTestID, "fleet")
	if _, err := db.RegisterRepository(registration); err != nil {
		t.Fatal(err)
	}
	otherID, err := model.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RegisterRepository(repositoryTestCommand(otherID, "occupied")); err != nil {
		t.Fatal(err)
	}
	before, _ := db.Status()
	for _, name := range []string{"", "UPPER", "../escape", "with space", " fleet ", "occupied"} {
		if _, err := db.RenameRepository(renameTestCommand(name, "fleet", name)); err == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
	for _, change := range []func(*RenameRepositoryCommand){
		func(c *RenameRepositoryCommand) { c.RepositoryID = "../escape" },
		func(c *RenameRepositoryCommand) { c.RepositoryID = "01AAAAAAAAAAAAAAAAAAAAAAAA" },
		func(c *RenameRepositoryCommand) { c.ProtocolVersion++ },
		func(c *RenameRepositoryCommand) { c.CreatedAt = time.Time{} },
		func(c *RenameRepositoryCommand) { c.ID = registration.ID },
		func(c *RenameRepositoryCommand) { c.PreviousName = "wrong" },
	} {
		c := renameTestCommand("invalid", "fleet", "new-name")
		change(&c)
		if _, err := db.RenameRepository(c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	after, _ := db.Status()
	if before.GlobalSequence != after.GlobalSequence {
		t.Fatal("rejected rename changed event history")
	}
	repos, _ := db.ListRepositories()
	for _, r := range repos {
		if r.ID == importTestID && r.Name != "fleet" {
			t.Fatal("failed rename changed registry")
		}
	}
}

func TestConcurrentRepositoryRenamesEnforceUniqueness(t *testing.T) {
	db, _ := itemDatabase(t)
	ids := []string{importTestID}
	other, err := model.NewID()
	if err != nil {
		t.Fatal(err)
	}
	ids = append(ids, other)
	names := []string{"one", "two"}
	for i, id := range ids {
		if _, err := db.RegisterRepository(repositoryTestCommand(id, names[i])); err != nil {
			t.Fatal(err)
		}
	}
	oneWinner(t, len(ids), func(i int) error {
		c := renameTestCommand(names[i], names[i], "shared")
		c.RepositoryID = ids[i]
		_, err := db.RenameRepository(c)
		return err
	})
}
