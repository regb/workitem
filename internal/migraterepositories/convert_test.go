package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/testutil"
	bolt "go.etcd.io/bbolt"
)

func conversionFixture(t *testing.T) (string, Plan) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wi.db")
	before := model.NewManifest(testutil.ID(t, 1), "task", "Task", nil, false, testutil.Time(), model.Repository{RootAtCreation: "/external", GitCommonDir: "/external/.git", CreatedFromCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, model.NewCheckout(model.WorkspaceKindManagedSlot, model.ExecutorLocal, "wi/task"))
	after := before
	after.Repository = model.Repository{ID: testutil.ID(t, 2), CreatedFromCommit: before.Repository.CreatedFromCommit}
	plan := Plan{CreatedAt: testutil.Time(), Repositories: []RepositoryPlan{{Record: migrationRepositoryRecord{OwnedRepository: model.OwnedRepository{ID: after.Repository.ID, Name: "repo", IntegrationBranch: "main", CreatedAt: testutil.Time()}, OriginURL: "git@example.com:legacy/repo.git"}, Source: "/external"}}, Items: []ItemPlan{{Before: before, After: after}}}
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"meta", "pending_native_writes", "projections", "domain_events", "domain_event_ids", "item_revisions", "command_results", "domain_commands", "import_sources", "import_offsets"} {
			if _, err := tx.CreateBucket([]byte(name)); err != nil {
				return err
			}
		}
		meta := tx.Bucket([]byte("meta"))
		if err := meta.Put([]byte("schema_version"), uintBytes(4)); err != nil {
			return err
		}
		if err := meta.Put([]byte("global_sequence"), uintBytes(1)); err != nil {
			return err
		}
		projections := tx.Bucket([]byte("projections"))
		manifests, err := projections.CreateBucket([]byte(coordinator.ManifestProjection))
		if err != nil {
			return err
		}
		body, _ := json.Marshal(before)
		if err := manifests.Put([]byte(before.ID), body); err != nil {
			return err
		}
		for _, name := range []string{coordinator.RuntimeOwnershipProjection, coordinator.TerminalOwnershipProjection, coordinator.AgentObservationProjection, "worktree_observation", coordinator.RuntimeActivityProjection} {
			b, err := projections.CreateBucket([]byte(name))
			if err != nil {
				return err
			}
			if err := b.Put([]byte(before.ID), []byte("old observation")); err != nil {
				return err
			}
		}
		if err := tx.Bucket([]byte("item_revisions")).Put([]byte(before.ID), uintBytes(1)); err != nil {
			return err
		}
		if err := tx.Bucket([]byte("domain_events")).Put(uintBytes(1), []byte("retained history")); err != nil {
			return err
		}
		for _, name := range []string{"command_results", "domain_commands", "import_sources", "import_offsets"} {
			if err := tx.Bucket([]byte(name)).Put([]byte("retained"), []byte("original bytes")); err != nil {
				return err
			}
		}
		return nil
	})
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("fixture: %v, close: %v", err, closeErr)
	}
	return path, plan
}

func TestConversionPreservesHistoryAndClearsOnlyObservations(t *testing.T) {
	path, plan := conversionFixture(t)
	convert(path, plan)
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.View(func(tx *bolt.Tx) error {
		if number(tx.Bucket([]byte("meta")).Get([]byte("schema_version"))) != 5 || number(tx.Bucket([]byte("meta")).Get([]byte("global_sequence"))) != 3 {
			t.Fatal("incorrect migrated schema or event sequence")
		}
		if string(tx.Bucket([]byte("domain_events")).Get(uintBytes(1))) != "retained history" {
			t.Fatal("rewrote historical event")
		}
		for _, name := range []string{"command_results", "domain_commands", "import_sources", "import_offsets"} {
			if string(tx.Bucket([]byte(name)).Get([]byte("retained"))) != "original bytes" {
				t.Fatalf("changed %s", name)
			}
		}
		projections := tx.Bucket([]byte("projections"))
		for _, name := range []string{coordinator.RuntimeOwnershipProjection, coordinator.TerminalOwnershipProjection, coordinator.AgentObservationProjection, "worktree_observation", coordinator.RuntimeActivityProjection} {
			if projections.Bucket([]byte(name)) != nil {
				t.Fatalf("retained stale %s", name)
			}
		}
		var after model.Manifest
		if err := json.Unmarshal(projections.Bucket([]byte(coordinator.ManifestProjection)).Get([]byte(plan.Items[0].Before.ID)), &after); err != nil {
			return err
		}
		if after.Repository.ID != plan.Repositories[0].Record.ID || after.Repository.CreatedFromCommit != plan.Items[0].Before.Repository.CreatedFromCommit {
			t.Fatal("lost repository identity or provenance")
		}
		if number(tx.Bucket([]byte("item_revisions")).Get([]byte(after.ID))) != 2 {
			t.Fatal("incorrect item revision")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConversionRollsBackChangedInventory(t *testing.T) {
	path, plan := conversionFixture(t)
	plan.Items[0].Before.Title = "stale inventory"
	func() {
		defer func() {
			if recover() == nil {
				t.Error("accepted stale inventory")
			}
		}()
		convert(path, plan)
	}()
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.View(func(tx *bolt.Tx) error {
		if number(tx.Bucket([]byte("meta")).Get([]byte("schema_version"))) != 4 || number(tx.Bucket([]byte("meta")).Get([]byte("global_sequence"))) != 1 || tx.Bucket([]byte("domain_events")).Stats().KeyN != 1 {
			t.Fatal("failed conversion partially committed")
		}
		if tx.Bucket([]byte("projections")).Bucket([]byte(coordinator.RepositoryProjection)) != nil {
			t.Fatal("failed conversion published registry")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
