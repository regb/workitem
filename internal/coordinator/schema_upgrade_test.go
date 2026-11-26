package coordinator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

// legacySchema returns a data root whose database is at schema version, after
// mutate wrote the state an older build left.
func legacySchema(t *testing.T, version uint64, mutate func(*Database)) string {
	t.Helper()
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(db)
	}
	if err := db.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketMeta).Put(keySchemaVersion, encodeUint64(version)) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

// upgradedDatabase opens a legacy root, which upgrades it, and checks the
// upgrade kept a backup of the old database.
func upgradedDatabase(t *testing.T, root string, from uint64) *Database {
	t.Helper()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := os.Stat(fmt.Sprintf("%s.schema-%d.bak", DatabasePath(root), from)); err != nil {
		t.Fatalf("pre-upgrade backup missing: %v", err)
	}
	return db
}

// Keep the fixture snapshot in the exact format tx.WriteTo publishes, rather
// than copying unused space from the database's allocation.
func legacyBackupFixture(t *testing.T) (string, []byte) {
	t.Helper()
	root := legacySchema(t, 5, nil)
	raw, err := bolt.Open(DatabasePath(root), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot bytes.Buffer
	if err := raw.View(func(tx *bolt.Tx) error { _, err := tx.WriteTo(&snapshot); return err }); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	return root, snapshot.Bytes()
}

func TestUpgradeRejectsUnsafeOrIncompleteBackupWithoutChangingDatabase(t *testing.T) {
	for _, kind := range []string{"empty", "truncated", "stale", "symlink", "hardlink", "directory", "fifo", "public"} {
		t.Run(kind, func(t *testing.T) {
			root, snapshot := legacyBackupFixture(t)
			backup := DatabasePath(root) + ".schema-5.bak"
			switch kind {
			case "empty":
				if err := os.WriteFile(backup, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := os.WriteFile(backup, snapshot[:len(snapshot)/2], 0600); err != nil {
					t.Fatal(err)
				}
			case "stale":
				if err := os.WriteFile(backup, snapshot, 0600); err != nil {
					t.Fatal(err)
				}
				raw, err := bolt.Open(DatabasePath(root), 0600, nil)
				if err != nil {
					t.Fatal(err)
				}
				err = raw.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketMeta).Put(keyGlobalSequence, encodeUint64(1)) })
				if err != nil {
					t.Fatal(err)
				}
				if err := raw.Close(); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(root, "original")
				if err := os.WriteFile(target, snapshot, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, backup); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(DatabasePath(root), backup); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(backup, 0700); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(backup, 0600); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.WriteFile(backup, snapshot, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(backup, 0644); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(DatabasePath(root))
			if err != nil {
				t.Fatal(err)
			}
			opened, err := OpenDatabase(root)
			if err == nil {
				opened.Close()
				t.Fatal("unsafe backup allowed migration")
			}
			if !strings.Contains(err.Error(), "before schema upgrade") {
				t.Fatalf("unexpected error: %v", err)
			}
			after, err := os.ReadFile(DatabasePath(root))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed backup validation changed the database", err)
			}
			if kind == "empty" || kind == "truncated" || kind == "stale" || kind == "public" {
				info, err := os.Stat(backup)
				if err != nil {
					t.Fatal(err)
				}
				if info.Size() == 0 && kind != "empty" {
					t.Fatal("existing backup was overwritten")
				}
			}
		})
	}
}

func TestUpgradeReusesOnlyMatchingBackupAndKeepsItPrivate(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "retry"}[existing], func(t *testing.T) {
			root, snapshot := legacyBackupFixture(t)
			backup := DatabasePath(root) + ".schema-5.bak"
			var before os.FileInfo
			if existing {
				if err := os.WriteFile(backup, snapshot, 0600); err != nil {
					t.Fatal(err)
				}
				before, _ = os.Stat(backup)
			}
			// A crash during copying can leave a temp file. It must not count as a backup.
			if err := os.WriteFile(filepath.Join(root, ".wi-schema-backup-orphan"), []byte("partial"), 0600); err != nil {
				t.Fatal(err)
			}
			db, err := OpenDatabase(root)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			status, err := db.Status()
			if err != nil || status.SchemaVersion != SchemaVersion {
				t.Fatal(status, err)
			}
			data, err := os.ReadFile(backup)
			if err != nil || !bytes.Equal(data, snapshot) {
				t.Fatal("backup is not the complete pre-upgrade snapshot", err)
			}
			info, err := os.Stat(backup)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("backup is not private", err)
			}
			if existing && !os.SameFile(before, info) {
				t.Fatal("retry replaced an existing valid backup")
			}
			original, err := bolt.Open(backup, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer original.Close()
			if err := original.View(func(tx *bolt.Tx) error {
				if decodeUint64(tx.Bucket(bucketMeta).Get(keySchemaVersion)) != 5 {
					return errors.New("backup was upgraded")
				}
				for err := range tx.Check() {
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSchemaBackupPublicationIsAtomicAndDoesNotOverwrite(t *testing.T) {
	for _, competing := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted-copy", true: "competing-publication"}[competing], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "backup")
			interrupted := errors.New("interrupted copy")
			err := createSchemaBackup(path, func(w io.Writer) error {
				if _, err := io.WriteString(w, "partial"); err != nil {
					return err
				}
				if competing {
					return os.WriteFile(path, []byte("keep this backup"), 0600)
				}
				return interrupted
			})
			if competing {
				if !errors.Is(err, os.ErrExist) {
					t.Fatal(err)
				}
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "keep this backup" {
					t.Fatal("existing backup overwritten", err)
				}
			} else {
				if !errors.Is(err, interrupted) {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("incomplete backup published", err)
				}
			}
			temps, err := filepath.Glob(filepath.Join(root, ".wi-schema-backup-*"))
			if err != nil || len(temps) != 0 {
				t.Fatal("temporary files leaked", temps, err)
			}
		})
	}
}

// manifestFields is the stored form of manifest, to edit as an older build
// wrote it.
func manifestFields(t *testing.T, manifest model.Manifest) map[string]any {
	t.Helper()
	encoded, _ := json.Marshal(manifest)
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func putLegacyManifest(t *testing.T, db *Database, id string, fields map[string]any) {
	t.Helper()
	encoded, _ := json.Marshal(fields)
	if err := db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection)).Put([]byte(id), encoded)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaFiveUpgradeReplacesWaitingWithBlockerAndKeepsBackup(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	legacy := map[string]string{importTestID: "working", "01KZYHGDCECSFS4BJ2SNTQP49W": "waiting", "01KZYHGDCECSFS4BJ2SNTQP49X": "archived", "01KZYHGDCECSFS4BJ2SNTQP49Y": "backlog"}
	root := legacySchema(t, 5, func(db *Database) {
		for id, state := range legacy {
			manifest := model.NewManifest(id, "legacy-"+state, "Legacy", nil, false, now, model.Repository{}, model.Checkout{})
			if state == "archived" {
				manifest.Slug = ""
			}
			fields := manifestFields(t, manifest)
			fields["state"] = state
			putLegacyManifest(t, db, id, fields)
		}
	})
	upgraded := upgradedDatabase(t, root, 5)
	want := map[string]string{importTestID: model.StateActive, "01KZYHGDCECSFS4BJ2SNTQP49W": model.StateActive, "01KZYHGDCECSFS4BJ2SNTQP49X": model.StateClosed, "01KZYHGDCECSFS4BJ2SNTQP49Y": model.StateBacklog}
	for id, state := range want {
		canonical, err := upgraded.CanonicalManifest(id)
		if err != nil || canonical.Manifest.State != state {
			t.Fatalf("%s state=%q err=%v", id, canonical.Manifest.State, err)
		}
		if _, err := store.NormalizeManifest(canonical.Manifest); err != nil {
			t.Fatalf("%s invalid after upgrade: %v", id, err)
		}
		if state == model.StateClosed {
			continue
		}
		var activity AttentionActivity
		if found, err := upgraded.ReadProjection(AttentionActivityProjection, id, &activity); err != nil || !found || activity.LastReadAt == nil {
			t.Fatalf("%s activity=%+v found=%v err=%v", id, activity, found, err)
		}
		events, err := upgraded.ListItemEvents(id)
		if err != nil || len(events) == 0 || events[len(events)-1].Type != "attention.read" {
			t.Fatalf("%s upgrade events=%+v err=%v", id, events, err)
		}
	}
	waiting, _ := upgraded.CanonicalManifest("01KZYHGDCECSFS4BJ2SNTQP49W")
	if len(waiting.Manifest.Blockers) != 1 || waiting.Manifest.Blockers[0].ID != MigratedWaitingBlockerID || waiting.Manifest.Blockers[0].Kind != model.BlockerExternal {
		t.Fatalf("waiting item blockers = %+v", waiting.Manifest.Blockers)
	}
	status, _ := upgraded.Status()
	if status.SchemaVersion != SchemaVersion {
		t.Fatalf("schema = %d", status.SchemaVersion)
	}
}

func TestSchemaSixUpgradeTurnsReasonsIntoEventNames(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fields := manifestFields(t, testManifest(importTestID, "legacy", "Legacy", now))
	until := now.Add(time.Hour)
	fields["blockers"] = []map[string]any{
		{"id": "bwaiting", "kind": "external", "reason": "Waiting (migrated from the former waiting state)", "created_at": now},
		{"id": "b1", "kind": "external", "reason": "API review", "created_at": now},
		{"id": "b2", "kind": "time", "until": until, "reason": "freeze", "created_at": now},
	}
	upgraded := upgradedDatabase(t, legacySchema(t, 6, func(db *Database) { putLegacyManifest(t, db, importTestID, fields) }), 6)
	canonical, err := upgraded.CanonicalManifest(importTestID)
	if err != nil {
		t.Fatal(err)
	}
	blockers := canonical.Manifest.Blockers
	if len(blockers) != 3 || blockers[0].Event != MigratedWaitingEvent || blockers[1].Event != "API review" || blockers[2].Event != "" || blockers[2].Until == nil {
		t.Fatalf("blockers = %+v", blockers)
	}
	for _, blocker := range blockers {
		if err := model.ValidateBlocker(blocker, importTestID, now.Add(-time.Minute)); err != nil {
			t.Fatalf("%+v invalid after upgrade: %v", blocker, err)
		}
	}
}
