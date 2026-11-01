package coordinator

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

func TestRepositoryEnvironmentKeepsValuesOutOfHistory(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.RegisterRepository(repositoryTestCommand(importTestID, "project")); err != nil {
		t.Fatal(err)
	}
	empty, err := db.RepositoryEnvironment("project")
	if err != nil || empty.Environment.Revision != 0 || len(empty.Environment.Vars) != 0 {
		t.Fatal(empty, err)
	}
	secret := "sk-very-secret"
	set, err := db.SetRepositoryEnvironment(SetRepositoryEnvironmentCommand{Selector: "project", Set: []model.EnvVar{{Key: "API_KEY", Value: secret}, {Key: "PORT", Value: "8080"}}})
	if err != nil || set.Environment.Revision != 1 {
		t.Fatal(set, err)
	}
	unset, err := db.SetRepositoryEnvironment(SetRepositoryEnvironmentCommand{Selector: importTestID, Set: []model.EnvVar{{Key: "PORT", Value: "9090"}}, Unset: []string{"API_KEY"}})
	if err != nil || unset.Environment.Revision != 2 || !reflect.DeepEqual(unset.Environment.Vars, []model.EnvVar{{Key: "PORT", Value: "9090"}}) {
		t.Fatal(unset, err)
	}
	// A save from an older read is refused; one from the current read wins.
	stale := uint64(1)
	if _, err := db.SetRepositoryEnvironment(SetRepositoryEnvironmentCommand{Selector: "project", ExpectedRevision: &stale, Replace: true}); err == nil {
		t.Fatal("stale replace accepted")
	}
	current := uint64(2)
	replaced, err := db.SetRepositoryEnvironment(SetRepositoryEnvironmentCommand{Selector: "project", ExpectedRevision: &current, Replace: true, Set: []model.EnvVar{{Key: "A", Value: "1"}}})
	if err != nil || replaced.Environment.Revision != 3 || len(replaced.Environment.Vars) != 1 {
		t.Fatal(replaced, err)
	}
	same, err := db.SetRepositoryEnvironment(SetRepositoryEnvironmentCommand{Selector: "project", Set: []model.EnvVar{{Key: "A", Value: "1"}}})
	if err != nil || same.Environment.Revision != 3 {
		t.Fatal("an unchanged set bumped the revision", same, err)
	}
	for _, bad := range [][]model.EnvVar{{{Key: "1A"}}, {{Key: "WI_ID"}}, {{Key: "TMUX"}}, {{Key: "A"}, {Key: "A"}}} {
		if _, err := db.SetRepositoryEnvironment(SetRepositoryEnvironmentCommand{Selector: "project", Set: bad}); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	events := 0
	if err := db.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketEvents).ForEach(func(_, value []byte) error {
			if bytes.Contains(value, []byte(secret)) || bytes.Contains(value, []byte("9090")) {
				t.Fatal("a value entered history")
			}
			if bytes.Contains(value, []byte("repository.environment_changed")) {
				events++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if events != 3 {
		t.Fatalf("events = %d", events)
	}
}
