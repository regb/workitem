package coordinator

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/sys/unix"
)

// Keep the fixture snapshot in the exact format tx.WriteTo publishes, rather
// than copying unused space from the database's allocation.
func legacyBackupFixture(t *testing.T) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketMeta).Put(keySchemaVersion, encodeUint64(5)) }); err != nil {
		t.Fatal(err)
	}
	var snapshot bytes.Buffer
	if err := db.db.View(func(tx *bolt.Tx) error { _, err := tx.WriteTo(&snapshot); return err }); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
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
