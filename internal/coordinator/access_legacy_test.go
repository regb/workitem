package coordinator

import (
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

// The entry installations used to start with, every item's agent managing
// every item, goes when the database opens; what a person granted stays.
func TestOpeningRemovesTheLegacyDefaultAccess(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	granted, err := db.GrantAccess(model.AccessEntry{Subject: model.EveryItemPrincipal, ItemID: model.EveryItem, Level: model.AccessView, GrantedBy: model.OperatorUser, GrantedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	legacy := model.AccessEntry{ID: legacyDefaultAccessID, Subject: model.EveryItemPrincipal, ItemID: model.EveryItem, Level: model.AccessManage, GrantedBy: model.SystemActor, GrantedAt: time.Now()}
	if err := db.db.Update(func(tx *bolt.Tx) error { return putProjectionValue(tx, AccessProjection, legacy.ID, legacy) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err = OpenDatabase(root); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries, err := db.AccessEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != granted.ID {
		t.Fatalf("entries after opening: %+v", entries)
	}
}
