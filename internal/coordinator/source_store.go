package coordinator

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

type SourceCheckpoint struct {
	Source          string    `json:"source"`
	Fingerprint     string    `json:"fingerprint"`
	FingerprintSize int64     `json:"fingerprint_size"`
	Offset          int64     `json:"offset"`
	Size            int64     `json:"size"`
	ModifiedAt      time.Time `json:"modified_at"`
	ObservedAt      time.Time `json:"observed_at"`
	RecordsSeen     uint64    `json:"records_seen"`
	RecordsImported uint64    `json:"records_imported"`
	RecordsSkipped  uint64    `json:"records_skipped"`
}

type SourceBatchResult struct {
	Imported      int    `json:"imported"`
	Duplicates    int    `json:"duplicates"`
	FinalSequence uint64 `json:"final_sequence"`
}

func validSourceKey(source string) bool {
	if source == "" || filepath.IsAbs(source) || filepath.Clean(source) != source {
		return false
	}
	for _, part := range strings.Split(source, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (d *Database) UpdateProjections(updates []ProjectionUpdate) error {
	return d.db.Update(func(tx *bolt.Tx) error {
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
		return nil
	})
}

func (d *Database) SourceCheckpoint(source string) (SourceCheckpoint, bool, error) {
	if !validSourceKey(source) {
		return SourceCheckpoint{}, false, errors.New("invalid source key")
	}
	var checkpoint SourceCheckpoint
	found := false
	err := d.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(bucketSourceOffsets).Get([]byte(source))
		if value == nil {
			return nil
		}
		found = true
		return json.Unmarshal(value, &checkpoint)
	})
	return checkpoint, found, err
}

// CommitSourceBatch atomically appends compact imported events, updates their
// projections, and advances the native-source checkpoint. Replayed event IDs
// are ignored, making replacement/truncation recovery idempotent.
func (d *Database) CommitSourceBatch(checkpoint SourceCheckpoint, pending []PendingEvent, updates []ProjectionUpdate) (SourceBatchResult, error) {
	if !validSourceKey(checkpoint.Source) || checkpoint.Offset < 0 || checkpoint.Size < 0 || checkpoint.FingerprintSize < 0 {
		return SourceBatchResult{}, errors.New("invalid source checkpoint")
	}
	var result SourceBatchResult
	err := d.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		events := tx.Bucket(bucketEvents)
		eventIDs := tx.Bucket(bucketEventIDs)
		revisions := tx.Bucket(bucketRevisions)
		sequence := decodeUint64(meta.Get(keyGlobalSequence))
		for _, value := range pending {
			if value.ID == "" || value.Type == "" {
				return errors.New("event id and type are required")
			}
			if eventIDs.Get([]byte(value.ID)) != nil {
				result.Duplicates++
				continue
			}
			sequence++
			revision := uint64(0)
			if value.ItemID != "" {
				revision = decodeUint64(revisions.Get([]byte(value.ItemID))) + 1
				if err := revisions.Put([]byte(value.ItemID), encodeUint64(revision)); err != nil {
					return err
				}
			}
			event := DomainEvent{Sequence: sequence, ID: value.ID, ItemID: value.ItemID, ItemRevision: revision, Type: value.Type, Timestamp: value.Timestamp, Actor: value.Actor, CausationID: "source:" + checkpoint.Source, Payload: value.Payload}
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
			result.Imported++
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
		checkpointBytes, err := json.Marshal(checkpoint)
		if err != nil {
			return err
		}
		if err := tx.Bucket(bucketSourceOffsets).Put([]byte(checkpoint.Source), checkpointBytes); err != nil {
			return err
		}
		if err := meta.Put(keyGlobalSequence, encodeUint64(sequence)); err != nil {
			return err
		}
		result.FinalSequence = sequence
		return nil
	})
	if err != nil {
		return SourceBatchResult{}, fmt.Errorf("commit source batch: %w", err)
	}
	return result, nil
}
