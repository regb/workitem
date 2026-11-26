package coordinator

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

func TestClosedSlugsAreRecoveredFromCloseEvents(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	db, _ := itemDatabase(t, testManifest(importTestID, "closing", "Closing", now))
	before, _ := db.CanonicalManifest(importTestID)
	if _, err := runCommand(t, db, ManifestCommand{ID: "close", Type: CommandStateSet, ItemID: importTestID, TargetState: model.StateClosed, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// An item closed before closed slugs were kept.
	if err := db.db.Update(func(tx *bolt.Tx) error {
		projection := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection))
		var m model.Manifest
		if err := json.Unmarshal(projection.Get([]byte(importTestID)), &m); err != nil {
			return err
		}
		m.ClosedSlug = ""
		data, _ := json.Marshal(m)
		return projection.Put([]byte(importTestID), data)
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := db.RecoverClosedSlugs(now); err != nil || n != 1 {
		t.Fatalf("recovered %d: %v", n, err)
	}
	if recovered, _ := db.CanonicalManifest(importTestID); recovered.Manifest.ClosedSlug != before.Manifest.Slug {
		t.Fatalf("recovered closed slug %q, want %q", recovered.Manifest.ClosedSlug, before.Manifest.Slug)
	}
	if n, err := db.RecoverClosedSlugs(now); err != nil || n != 0 {
		t.Fatalf("second pass recovered %d: %v", n, err)
	}
}
