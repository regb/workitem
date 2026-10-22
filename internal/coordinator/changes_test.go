package coordinator

import (
	"reflect"
	"testing"
	"time"
)

func TestChangeLogReportsChangedItemsAndWakesWaiters(t *testing.T) {
	log := newChangeLog()
	first, _ := log.since("", 0)
	if !first.All || first.Revision == 0 {
		t.Fatalf("a first request must take everything: %+v", first)
	}
	current, waiter := log.since(first.Epoch, first.Revision)
	if current.All || len(current.ItemIDs) != 0 {
		t.Fatalf("nothing changed yet: %+v", current)
	}
	log.record("a", "b")
	log.record("a")
	select {
	case <-waiter:
	case <-time.After(time.Second):
		t.Fatal("a change did not wake the waiter")
	}
	changed, _ := log.since(first.Epoch, first.Revision)
	if !reflect.DeepEqual(changed.ItemIDs, []string{"a", "b"}) || changed.Revision != first.Revision+2 {
		t.Fatalf("changed = %+v", changed)
	}
	// A change to everything (a repository rename) is reported as such.
	before := changed.Revision
	log.recordAll()
	if all, _ := log.since(first.Epoch, before); !all.All {
		t.Fatal("a change to every item was reported per item")
	}
	// Another daemon instance, or a caller too far behind, takes everything.
	if other, _ := log.since("other", first.Revision); !other.All {
		t.Fatal("a revision from another daemon was trusted")
	}
	for i := 0; i < maxRecentChanges+1; i++ {
		log.record("c")
	}
	if behind, _ := log.since(first.Epoch, first.Revision); !behind.All {
		t.Fatal("a caller behind the remembered changes was not told to take everything")
	}
}

func TestCommitsRecordTheirItems(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start, _ := db.changes.since("", 0)
	if _, err := db.Commit(Command{ID: "c1", Type: "test", ItemID: importTestID, ReceivedAt: time.Now()}, []PendingEvent{{ID: "e1", ItemID: importTestID, Type: "test.event", Timestamp: time.Now()}}, nil); err != nil {
		t.Fatal(err)
	}
	// Replaying a command commits nothing new.
	if _, err := db.Commit(Command{ID: "c1", Type: "test", ItemID: importTestID, ReceivedAt: time.Now()}, []PendingEvent{{ID: "e1", ItemID: importTestID, Type: "test.event", Timestamp: time.Now()}}, nil); err != nil {
		t.Fatal(err)
	}
	changed, _ := db.changes.since(start.Epoch, start.Revision)
	if !reflect.DeepEqual(changed.ItemIDs, []string{importTestID}) || changed.Revision != start.Revision+1 {
		t.Fatalf("changed = %+v", changed)
	}
}
