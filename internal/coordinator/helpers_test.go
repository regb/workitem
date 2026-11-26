package coordinator

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
)

const importTestID = "01KZYHGDCECSFS4BJ2SNTQP49V"

// testManifest is an active fixture item with no repository or checkout.
func testManifest(id, slug, title string, now time.Time) model.Manifest {
	manifest := model.NewManifest(id, slug, title, nil, false, now, model.Repository{}, model.Checkout{})
	manifest.State = model.StateActive
	return manifest
}

// importItems imports manifests into the projection as the daemon's startup
// import does.
func importItems(t *testing.T, db *Database, manifests ...model.Manifest) {
	t.Helper()
	values := map[string]ImportedManifest{}
	for _, manifest := range manifests {
		values[manifest.ID] = ImportedManifest{Manifest: manifest, Digest: "fixture-" + manifest.ID}
	}
	if _, err := db.SyncImportedManifests(values, true); err != nil {
		t.Fatal(err)
	}
}

// itemDatabase opens a database in a fresh data root holding manifests. It is
// closed when the test ends.
func itemDatabase(t *testing.T, manifests ...model.Manifest) (*Database, string) {
	t.Helper()
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	importItems(t, db, manifests...)
	return db, root
}

// seedItems imports manifests into the database of root and closes it, so a
// server can then open it.
func seedItems(t *testing.T, root string, manifests ...model.Manifest) {
	t.Helper()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	importItems(t, db, manifests...)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// oneWinner runs attempt concurrently n times and fails unless exactly one
// attempt succeeds. It returns the failures.
func oneWinner(t *testing.T, n int, attempt func(int) error) []error {
	t.Helper()
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = attempt(i)
		}()
	}
	wg.Wait()
	var failures []error
	for _, err := range errs {
		if err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != n-1 {
		t.Fatalf("%d of %d concurrent attempts succeeded: %v", n-len(failures), n, failures)
	}
	return failures
}

// testItems are active fixture items with the given IDs.
func testItems(now time.Time, ids ...string) []model.Manifest {
	manifests := make([]model.Manifest, 0, len(ids))
	for index, id := range ids {
		manifests = append(manifests, testManifest(id, "item-"+string(rune('a'+index)), "Item", now))
	}
	return manifests
}

// seedRuntimeOwner records a running runtime as the owner of the item.
func seedRuntimeOwner(t *testing.T, db *Database, itemID, runtimeID string) {
	t.Helper()
	saveRuntimeOwner(t, db, model.AgentRuntime{ID: runtimeID, WorkItemID: itemID, State: model.AgentRuntimeRunning})
}

func saveRuntimeOwner(t *testing.T, db *Database, runtime model.AgentRuntime) {
	t.Helper()
	encoded, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateProjections([]ProjectionUpdate{{Projection: RuntimeOwnershipProjection, Key: runtime.WorkItemID, Value: encoded}}); err != nil {
		t.Fatal(err)
	}
}
