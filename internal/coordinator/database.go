package coordinator

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

const (
	SchemaVersion = 3
	DatabaseFile  = "wi.db"
)

var (
	bucketMeta           = []byte("meta")
	bucketCommands       = []byte("commands")
	bucketEvents         = []byte("domain_events")
	bucketEventIDs       = []byte("domain_event_ids")
	bucketRevisions      = []byte("item_revisions")
	bucketProjections    = []byte("projections")
	bucketImportSources  = []byte("import_sources")
	bucketSourceOffsets  = []byte("source_offsets")
	bucketDomainCommands = []byte("domain_commands")
	bucketPendingWrites  = []byte("pending_native_writes")
	keySchemaVersion     = []byte("schema_version")
	keyGlobalSequence    = []byte("global_sequence")
)

type Database struct {
	path string
	db   *bolt.DB
}

type DatabaseStatus struct {
	Path           string `json:"path"`
	SchemaVersion  uint64 `json:"schema_version"`
	GlobalSequence uint64 `json:"global_sequence"`
}

type Command struct {
	ID               string          `json:"id"`
	ProtocolVersion  int             `json:"protocol_version"`
	Type             string          `json:"type"`
	Actor            string          `json:"actor"`
	ItemID           string          `json:"item_id,omitempty"`
	ExpectedRevision *uint64         `json:"expected_revision,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	ReceivedAt       time.Time       `json:"received_at"`
}

type PendingEvent struct {
	ID        string          `json:"id"`
	ItemID    string          `json:"item_id,omitempty"`
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Actor     string          `json:"actor"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type DomainEvent struct {
	Sequence     uint64          `json:"sequence"`
	ID           string          `json:"id"`
	ItemID       string          `json:"item_id,omitempty"`
	ItemRevision uint64          `json:"item_revision,omitempty"`
	Type         string          `json:"type"`
	Timestamp    time.Time       `json:"timestamp"`
	Actor        string          `json:"actor"`
	CausationID  string          `json:"causation_id"`
	Payload      json.RawMessage `json:"payload,omitempty"`
}

// toModelEvent adapts a compact domain event to the public model.Event
// used by durable-item views. Compact payloads are surfaced as event data when
// they decode to an object.
func (e DomainEvent) toModelEvent() model.Event {
	event := model.Event{Time: e.Timestamp, Type: e.Type, Actor: e.Actor}
	if len(e.Payload) > 0 {
		var data map[string]any
		if json.Unmarshal(e.Payload, &data) == nil {
			event.Data = data
		}
	}
	return event
}

type ProjectionUpdate struct {
	Projection string          `json:"projection"`
	Key        string          `json:"key"`
	Value      json.RawMessage `json:"value,omitempty"`
	Delete     bool            `json:"delete,omitempty"`
}

type CommitResult struct {
	CommandID     string        `json:"command_id"`
	Events        []DomainEvent `json:"events"`
	FinalSequence uint64        `json:"final_sequence"`
	ItemRevision  uint64        `json:"item_revision,omitempty"`
	CommittedAt   time.Time     `json:"committed_at"`
	Duplicate     bool          `json:"duplicate,omitempty"`
}

func DatabasePath(dataRoot string) string { return filepath.Join(dataRoot, DatabaseFile) }

func OpenDatabase(dataRoot string) (*Database, error) {
	if dataRoot == "" {
		return nil, errors.New("data root is required")
	}
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create data root: %w", err)
	}
	if err := os.Chmod(dataRoot, 0o700); err != nil {
		return nil, fmt.Errorf("secure data root: %w", err)
	}
	path := DatabasePath(dataRoot)
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open coordinator database: %w", err)
	}
	result := &Database{path: path, db: db}
	if err := result.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure coordinator database: %w", err)
	}
	return result, nil
}

func (d *Database) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

func (d *Database) initialize() error {
	return d.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMeta, bucketCommands, bucketEvents, bucketEventIDs, bucketRevisions, bucketProjections, bucketImportSources, bucketSourceOffsets, bucketDomainCommands, bucketPendingWrites} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		projections := tx.Bucket(bucketProjections)
		if _, err := projections.CreateBucketIfNotExists([]byte(ManifestProjection)); err != nil {
			return err
		}
		meta := tx.Bucket(bucketMeta)
		schema := decodeUint64(meta.Get(keySchemaVersion))
		if schema > SchemaVersion {
			return fmt.Errorf("coordinator database schema %d is newer than supported schema %d", schema, SchemaVersion)
		}
		if schema > 0 && schema < SchemaVersion {
			return fmt.Errorf("coordinator database schema %d is older than supported schema %d", schema, SchemaVersion)
		}
		if schema == 0 || schema < SchemaVersion {
			if err := meta.Put(keySchemaVersion, encodeUint64(SchemaVersion)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *Database) EventExists(eventID string) bool {
	if eventID == "" {
		return false
	}
	found := false
	_ = d.db.View(func(tx *bolt.Tx) error {
		found = tx.Bucket(bucketEventIDs).Get([]byte(eventID)) != nil
		return nil
	})
	return found
}

func (d *Database) Status() (DatabaseStatus, error) {
	status := DatabaseStatus{Path: d.path}
	err := d.db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		status.SchemaVersion = decodeUint64(meta.Get(keySchemaVersion))
		status.GlobalSequence = decodeUint64(meta.Get(keyGlobalSequence))
		return nil
	})
	return status, err
}

// Commit atomically records an idempotent command, appends globally ordered
// domain events, advances per-item revisions, and updates materialized
// projections. It is the storage primitive on which domain command handlers
// are built; clients never invoke it directly over the public daemon protocol.
func (d *Database) Commit(command Command, pending []PendingEvent, updates []ProjectionUpdate) (CommitResult, error) {
	if command.ID == "" || command.Type == "" {
		return CommitResult{}, errors.New("command id and type are required")
	}
	var result CommitResult
	err := d.db.Update(func(tx *bolt.Tx) error {
		commands := tx.Bucket(bucketCommands)
		if existing := commands.Get([]byte(command.ID)); existing != nil {
			if err := json.Unmarshal(existing, &result); err != nil {
				return fmt.Errorf("decode prior command outcome: %w", err)
			}
			result.Duplicate = true
			return nil
		}
		revisions := tx.Bucket(bucketRevisions)
		currentRevision := decodeUint64(revisions.Get([]byte(command.ItemID)))
		if command.ExpectedRevision != nil && *command.ExpectedRevision != currentRevision {
			return fmt.Errorf("item revision conflict: expected %d, current %d", *command.ExpectedRevision, currentRevision)
		}
		meta, events, eventIDs := tx.Bucket(bucketMeta), tx.Bucket(bucketEvents), tx.Bucket(bucketEventIDs)
		sequence := decodeUint64(meta.Get(keyGlobalSequence))
		result = CommitResult{CommandID: command.ID, Events: make([]DomainEvent, 0, len(pending)), CommittedAt: time.Now().UTC()}
		for _, value := range pending {
			if value.ID == "" || value.Type == "" {
				return errors.New("event id and type are required")
			}
			if eventIDs.Get([]byte(value.ID)) != nil {
				return fmt.Errorf("duplicate event id %s", value.ID)
			}
			sequence++
			itemRevision := uint64(0)
			if value.ItemID != "" {
				itemRevision = decodeUint64(revisions.Get([]byte(value.ItemID))) + 1
				if err := revisions.Put([]byte(value.ItemID), encodeUint64(itemRevision)); err != nil {
					return err
				}
				if value.ItemID == command.ItemID {
					result.ItemRevision = itemRevision
				}
			}
			event := DomainEvent{Sequence: sequence, ID: value.ID, ItemID: value.ItemID, ItemRevision: itemRevision, Type: value.Type, Timestamp: value.Timestamp, Actor: value.Actor, CausationID: command.ID, Payload: value.Payload}
			encoded, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if err := events.Put(encodeUint64(sequence), encoded); err != nil {
				return err
			}
			if err := eventIDs.Put([]byte(value.ID), encodeUint64(sequence)); err != nil {
				return err
			}
			result.Events = append(result.Events, event)
		}
		for _, update := range updates {
			if update.Projection == "" || update.Key == "" {
				return errors.New("projection and key are required")
			}
			projection, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(update.Projection))
			if err != nil {
				return err
			}
			if update.Delete {
				err = projection.Delete([]byte(update.Key))
			} else {
				err = projection.Put([]byte(update.Key), append([]byte(nil), update.Value...))
			}
			if err != nil {
				return err
			}
		}
		if err := meta.Put(keyGlobalSequence, encodeUint64(sequence)); err != nil {
			return err
		}
		result.FinalSequence = sequence
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return commands.Put([]byte(command.ID), encoded)
	})
	return result, err
}

const ManifestProjection = "manifests"

type ImportedManifest struct {
	Manifest model.Manifest
	Digest   string
}

type ManifestSyncResult struct {
	Imported       int      `json:"imported"`
	Unchanged      int      `json:"unchanged"`
	Removed        int      `json:"removed"`
	GlobalSequence uint64   `json:"global_sequence"`
	Warnings       []string `json:"warnings,omitempty"`
}

// SyncImportedManifests seeds the manifest projection and emits compact
// manifest events in one transaction. Removals occur only after a complete
// source scan so a missing manifest cannot be mistaken for a deletion. It is
// used by tests and the initial projection seed.
func (d *Database) SyncImportedManifests(values map[string]ImportedManifest, scanComplete bool) (ManifestSyncResult, error) {
	result := ManifestSyncResult{Warnings: []string{}}
	err := d.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		events := tx.Bucket(bucketEvents)
		revisions := tx.Bucket(bucketRevisions)
		sources := tx.Bucket(bucketImportSources)
		projection, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(ManifestProjection))
		if err != nil {
			return err
		}
		sequence := decodeUint64(meta.Get(keyGlobalSequence))
		ids := make([]string, 0, len(values))
		for id := range values {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			value := values[id]
			if string(sources.Get([]byte(id))) == value.Digest && projection.Get([]byte(id)) != nil {
				result.Unchanged++
				continue
			}
			encoded, err := json.Marshal(value.Manifest)
			if err != nil {
				return fmt.Errorf("encode imported manifest %s: %w", id, err)
			}
			sequence++
			revision := decodeUint64(revisions.Get([]byte(id))) + 1
			if err := revisions.Put([]byte(id), encodeUint64(revision)); err != nil {
				return err
			}
			event := importedManifestEvent(sequence, revision, id, "manifest.imported", value.Digest)
			eventBytes, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if err := events.Put(encodeUint64(sequence), eventBytes); err != nil {
				return err
			}
			if err := tx.Bucket(bucketEventIDs).Put([]byte(event.ID), encodeUint64(sequence)); err != nil {
				return err
			}
			if err := projection.Put([]byte(id), encoded); err != nil {
				return err
			}
			if err := sources.Put([]byte(id), []byte(value.Digest)); err != nil {
				return err
			}
			result.Imported++
		}
		if scanComplete {
			stale := []string{}
			if err := sources.ForEach(func(key, _ []byte) error {
				if _, ok := values[string(key)]; !ok {
					stale = append(stale, string(key))
				}
				return nil
			}); err != nil {
				return err
			}
			sort.Strings(stale)
			for _, id := range stale {
				sequence++
				revision := decodeUint64(revisions.Get([]byte(id))) + 1
				if err := revisions.Put([]byte(id), encodeUint64(revision)); err != nil {
					return err
				}
				event := importedManifestEvent(sequence, revision, id, "manifest.removed", string(sources.Get([]byte(id))))
				eventBytes, err := json.Marshal(event)
				if err != nil {
					return err
				}
				if err := events.Put(encodeUint64(sequence), eventBytes); err != nil {
					return err
				}
				if err := tx.Bucket(bucketEventIDs).Put([]byte(event.ID), encodeUint64(sequence)); err != nil {
					return err
				}
				if err := projection.Delete([]byte(id)); err != nil {
					return err
				}
				if err := sources.Delete([]byte(id)); err != nil {
					return err
				}
				result.Removed++
			}
		}
		if err := meta.Put(keyGlobalSequence, encodeUint64(sequence)); err != nil {
			return err
		}
		result.GlobalSequence = sequence
		return nil
	})
	return result, err
}

func importedManifestEvent(sequence, revision uint64, itemID, eventType, digest string) DomainEvent {
	identity := fmt.Sprintf("%s:%s:%d:%s", eventType, itemID, revision, digest)
	sum := sha256.Sum256([]byte(identity))
	id := "seed-" + hex.EncodeToString(sum[:16])
	payload, _ := json.Marshal(map[string]string{"source": "manifest", "digest": digest})
	return DomainEvent{Sequence: sequence, ID: id, ItemID: itemID, ItemRevision: revision, Type: eventType, Timestamp: time.Now().UTC(), Actor: "wi", CausationID: id, Payload: payload}
}

// ListItemEvents returns every compact domain event for one work item in
// commit order. Callers that need public model.Event values convert via
// DomainEvent.toModelEvent.
func (d *Database) ListItemEvents(itemID string) ([]DomainEvent, error) {
	if !model.ValidID(itemID) {
		return nil, fmt.Errorf("invalid work item id %q", itemID)
	}
	events := []DomainEvent{}
	err := d.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketEvents).ForEach(func(_, value []byte) error {
			var event DomainEvent
			if err := json.Unmarshal(value, &event); err != nil {
				return err
			}
			if event.ItemID == itemID {
				events = append(events, event)
			}
			return nil
		})
	})
	return events, err
}

func (d *Database) ListManifests() ([]model.Manifest, error) {
	values, err := d.ListProjection(ManifestProjection)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	manifests := make([]model.Manifest, 0, len(ids))
	for _, id := range ids {
		var manifest model.Manifest
		if err := json.Unmarshal(values[id], &manifest); err != nil {
			return nil, fmt.Errorf("decode manifest projection %s: %w", id, err)
		}
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}

func (d *Database) ListProjection(projection string) (map[string]json.RawMessage, error) {
	if projection == "" {
		return nil, errors.New("projection is required")
	}
	values := map[string]json.RawMessage{}
	err := d.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketProjections).Bucket([]byte(projection))
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(key, value []byte) error {
			if value != nil {
				values[string(key)] = append(json.RawMessage(nil), value...)
			}
			return nil
		})
	})
	return values, err
}

func (d *Database) ReadProjection(projection, key string, target any) (bool, error) {
	if projection == "" || key == "" {
		return false, errors.New("projection and key are required")
	}
	var value []byte
	err := d.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketProjections).Bucket([]byte(projection))
		if bucket == nil {
			return nil
		}
		if stored := bucket.Get([]byte(key)); stored != nil {
			value = append([]byte(nil), stored...)
		}
		return nil
	})
	if err != nil || value == nil {
		return false, err
	}
	if err := json.Unmarshal(value, target); err != nil {
		return false, err
	}
	return true, nil
}

func encodeUint64(value uint64) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	return encoded
}

func decodeUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}
