package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

// MigratedWaitingBlockerID identifies the external blocker that replaces the
// former waiting lifecycle state during the schema 5 to 6 upgrade; its event
// is MigratedWaitingEvent.
const (
	MigratedWaitingBlockerID = "bwaiting"
	MigratedWaitingEvent     = "waiting"
	migratedWaitingReason    = "Waiting (migrated from the former waiting state)"
)

// backupBeforeUpgrade keeps a complete snapshot of the database that the
// in-place upgrade is about to rewrite. It never overwrites an earlier backup.
func (d *Database) backupBeforeUpgrade() error {
	return d.db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		if meta == nil {
			return nil
		}
		schema := decodeUint64(meta.Get(keySchemaVersion))
		if schema == 0 || schema >= SchemaVersion {
			return nil
		}
		backup := fmt.Sprintf("%s.schema-%d.bak", d.path, schema)
		err := createSchemaBackup(backup, func(w io.Writer) error {
			_, err := tx.WriteTo(w)
			return err
		})
		if errors.Is(err, os.ErrExist) {
			// A previous attempt may have stopped before upgrading. Only reuse
			// its backup if it contains this exact pre-upgrade snapshot.
			expected := sha256.New()
			if _, hashErr := tx.WriteTo(expected); hashErr != nil {
				return fmt.Errorf("hash pre-upgrade snapshot: %w", hashErr)
			}
			err = validateSchemaBackup(backup, d.path, expected.Sum(nil))
		}
		if err != nil {
			return fmt.Errorf("back up coordinator database before schema upgrade: %w", err)
		}
		directory, err := os.Open(filepath.Dir(backup))
		if err != nil {
			return err
		}
		defer directory.Close()
		return directory.Sync()
	})
}

// createSchemaBackup publishes a complete, synced private file without
// replacing an existing backup. A failed copy leaves only a temporary file,
// never a partially written final backup. The caller syncs the directory.
func createSchemaBackup(path string, write func(io.Writer) error) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("backup already exists: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".wi-schema-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := write(temp); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// Link publishes atomically like rename, but cannot overwrite a backup
	// that appeared while the snapshot was being copied.
	if err := os.Link(temp.Name(), path); err != nil {
		return err
	}
	return os.Remove(temp.Name())
}

func validateSchemaBackup(path, source string, expected []byte) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open existing backup %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("existing backup %s must be a private regular file; preserve it elsewhere before retrying", path)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	if os.SameFile(info, sourceInfo) {
		return fmt.Errorf("existing backup %s shares the live database inode; preserve it elsewhere before retrying", path)
	}
	actual := sha256.New()
	if _, err := io.Copy(actual, file); err != nil {
		return err
	}
	if hex.EncodeToString(actual.Sum(nil)) != hex.EncodeToString(expected) {
		return fmt.Errorf("existing backup %s is incomplete or does not match the current database; preserve it elsewhere before retrying", path)
	}
	return file.Sync()
}

// upgradeSchema5To6 replaces the working|waiting|archived lifecycle with
// active|closed plus blockers. A waiting item becomes an active item with one
// external blocker, so it stays out of the attention queue until cleared.
// Existing agent responses are marked read at upgrade time so the new unread
// signal starts from a clean slate instead of surfacing every blocked item.
// Both facts are ordinary item events, so history and every activity reader
// agree on them.
func upgradeSchema5To6(tx *bolt.Tx, now time.Time) error {
	manifests := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection))
	if manifests == nil {
		return nil
	}
	upgraded := []model.Manifest{}
	err := manifests.ForEach(func(key, value []byte) error {
		var manifest model.Manifest
		if err := json.Unmarshal(value, &manifest); err != nil {
			return fmt.Errorf("decode manifest %s: %w", key, err)
		}
		upgraded = append(upgraded, manifest)
		return nil
	})
	if err != nil {
		return err
	}
	event := func(eventType string, data map[string]any) commandEvent {
		data["cause"] = "schema_upgrade"
		payload, _ := json.Marshal(data)
		return commandEvent{Type: eventType, Payload: payload}
	}
	for _, manifest := range upgraded {
		pending := []commandEvent{}
		switch manifest.State {
		case model.LegacyStateWorking:
			manifest.State = model.StateActive
		case model.LegacyStateWaiting:
			manifest.State = model.StateActive
			blocker := model.Blocker{ID: MigratedWaitingBlockerID, Kind: model.BlockerExternal, Event: MigratedWaitingEvent, CreatedAt: manifest.StateChangedAt.UTC()}
			manifest.Blockers = append(manifest.Blockers, blocker)
			pending = append(pending, event("blocker.added", blockerEventData(blocker)))
		case model.LegacyStateArchived:
			manifest.State = model.StateClosed
		case model.StateBacklog, model.StateActive, model.StateClosed:
		default:
			return fmt.Errorf("manifest %s has unknown state %q", manifest.ID, manifest.State)
		}
		if manifest.State != model.StateClosed {
			pending = append(pending, event("attention.read", map[string]any{"at": now}))
		}
		if len(pending) == 0 {
			// Closed items need no history entry; rewrite only the projection.
			encoded, err := json.Marshal(manifest)
			if err != nil {
				return err
			}
			if err := manifests.Put([]byte(manifest.ID), encoded); err != nil {
				return err
			}
			digest := sha256.Sum256(encoded)
			if err := tx.Bucket(bucketImportSources).Put([]byte(manifest.ID), []byte(hex.EncodeToString(digest[:]))); err != nil {
				return err
			}
			continue
		}
		if _, _, err := commitManifestEvents(tx, manifest, "schema-upgrade-6-"+manifest.ID, "wi", now, pending); err != nil {
			return err
		}
	}
	return nil
}

// upgradeSchema6To7 replaces free-form blocker reasons with event names.
// External blockers keep their text as the event they wait for; time and item
// blockers drop it, since their condition already says why they wait.
func upgradeSchema6To7(tx *bolt.Tx) error {
	manifests := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection))
	if manifests == nil {
		return nil
	}
	type update struct {
		key   []byte
		value []byte
	}
	updates := []update{}
	err := manifests.ForEach(func(key, value []byte) error {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(value, &fields); err != nil {
			return fmt.Errorf("decode manifest %s: %w", key, err)
		}
		raw, ok := fields["blockers"]
		if !ok {
			return nil
		}
		var blockers []map[string]any
		if err := json.Unmarshal(raw, &blockers); err != nil {
			return fmt.Errorf("decode blockers of %s: %w", key, err)
		}
		for _, blocker := range blockers {
			reason, _ := blocker["reason"].(string)
			delete(blocker, "reason")
			if blocker["kind"] != model.BlockerExternal {
				continue
			}
			if reason == "" || reason == migratedWaitingReason {
				reason = MigratedWaitingEvent
			}
			if len(reason) > model.MaxBlockerEventLength {
				reason = reason[:model.MaxBlockerEventLength]
			}
			blocker["event"] = reason
		}
		encodedBlockers, err := json.Marshal(blockers)
		if err != nil {
			return err
		}
		fields["blockers"] = encodedBlockers
		encoded, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		var manifest model.Manifest
		if err := json.Unmarshal(encoded, &manifest); err != nil {
			return fmt.Errorf("decode upgraded manifest %s: %w", key, err)
		}
		canonical, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		updates = append(updates, update{key: append([]byte(nil), key...), value: canonical})
		return nil
	})
	if err != nil {
		return err
	}
	for _, item := range updates {
		if err := manifests.Put(item.key, item.value); err != nil {
			return err
		}
		digest := sha256.Sum256(item.value)
		if err := tx.Bucket(bucketImportSources).Put(item.key, []byte(hex.EncodeToString(digest[:]))); err != nil {
			return err
		}
	}
	return nil
}

// upgradeSchema7To8 moves repository setting overrides from the former
// extensions.NAME namespaces to plugins.NAME, matching the merged
// [plugins.NAME] configuration table. Values and revisions are unchanged.
func upgradeSchema7To8(tx *bolt.Tx) error {
	settings := tx.Bucket(bucketProjections).Bucket([]byte(RepositorySettingsProjection))
	if settings == nil {
		return nil
	}
	type update struct {
		key   []byte
		value []byte
	}
	updates := []update{}
	err := settings.ForEach(func(key, value []byte) error {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(value, &fields); err != nil {
			return fmt.Errorf("decode repository settings %s: %w", key, err)
		}
		var namespaces map[string]json.RawMessage
		if raw, ok := fields["namespaces"]; ok {
			if err := json.Unmarshal(raw, &namespaces); err != nil {
				return fmt.Errorf("decode repository settings namespaces of %s: %w", key, err)
			}
		}
		renamed := map[string]json.RawMessage{}
		changed := false
		for namespace, entries := range namespaces {
			if name, ok := strings.CutPrefix(namespace, "extensions."); ok {
				namespace, changed = "plugins."+name, true
			}
			if _, exists := renamed[namespace]; exists {
				return fmt.Errorf("repository settings %s has duplicate namespace %s", key, namespace)
			}
			renamed[namespace] = entries
		}
		if !changed {
			return nil
		}
		encoded, err := json.Marshal(renamed)
		if err != nil {
			return err
		}
		fields["namespaces"] = encoded
		record, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		updates = append(updates, update{key: append([]byte(nil), key...), value: record})
		return nil
	})
	if err != nil {
		return err
	}
	for _, item := range updates {
		if err := settings.Put(item.key, item.value); err != nil {
			return err
		}
	}
	return nil
}
