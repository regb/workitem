package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
)

const importTestID = "01KZYHGDCECSFS4BJ2SNTQP49V"

// seedRoot opens, seeds, and closes a database so server-level tests can start
// NewServer against a projection that already contains their fixture items.
func seedRoot(ctx context.Context, root string) error {
	db, err := OpenDatabase(root)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = seedManifests(ctx, db, root)
	return err
}

// seedManifests seeds the manifest projection from on-disk manifests created by
// test fixtures that still use the file store as a convenient fixture builder.
func seedManifests(ctx context.Context, db *Database, dataRoot string) (ManifestSyncResult, error) {
	native := store.New(dataRoot)
	entries, err := os.ReadDir(native.ItemsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return db.SyncImportedManifests(map[string]ImportedManifest{}, true)
		}
		return ManifestSyncResult{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	values := map[string]ImportedManifest{}
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || !model.ValidID(id) {
			continue
		}
		manifest, err := native.LoadManifest(id)
		if err != nil {
			continue
		}
		encoded, _ := json.Marshal(manifest)
		digest := sha256.Sum256(encoded)
		values[id] = ImportedManifest{Manifest: manifest, Digest: hex.EncodeToString(digest[:])}
	}
	return db.SyncImportedManifests(values, true)
}
