package coordinator

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/model"
	"github.com/regb/workitem/internal/store"
	bolt "go.etcd.io/bbolt"
)

// AttentionActivityResult is a compact snapshot, not an event-history replay.
type AttentionActivityResult struct {
	Projection ProjectionMetadata `json:"projection"`
	Found      bool               `json:"found"`
	Activity   AttentionActivity  `json:"activity"`
}

func readAttentionActivityTx(tx *bolt.Tx, itemID string) (AttentionActivity, bool, error) {
	empty := AttentionActivity{WorkItemID: itemID}
	bucket := tx.Bucket(bucketProjections).Bucket([]byte(AttentionActivityProjection))
	if bucket == nil {
		return empty, false, nil
	}
	data := bucket.Get([]byte(itemID))
	if data == nil {
		return empty, false, nil
	}
	var activity AttentionActivity
	if err := json.Unmarshal(data, &activity); err != nil {
		return empty, false, fmt.Errorf("decode attention activity: %w", err)
	}
	if activity.WorkItemID != itemID {
		return empty, false, fmt.Errorf("attention activity identity mismatch")
	}
	return activity, true, nil
}

func putAttentionActivityTx(tx *bolt.Tx, activity AttentionActivity) error {
	bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(AttentionActivityProjection))
	if err != nil {
		return err
	}
	data, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	return bucket.Put([]byte(activity.WorkItemID), data)
}

// Marker writes and their history event share the same transaction. All
// marker timestamps are monotonic, including out-of-order defer requests.
func recordAttentionMarkerTx(tx *bolt.Tx, itemID, eventType string, at time.Time) error {
	activity, _, err := readAttentionActivityTx(tx, itemID)
	if err != nil {
		return err
	}
	timestamp := ptrTime(at.UTC())
	switch eventType {
	case "attention.read":
		activity.LastReadAt = laterTime(activity.LastReadAt, timestamp)
	case "attention.unread":
		activity.LastUnreadAt = laterTime(activity.LastUnreadAt, timestamp)
	case "attention.deferred":
		activity.LastDeferredAt = laterTime(activity.LastDeferredAt, timestamp)
	default:
		return fmt.Errorf("invalid attention marker event %q", eventType)
	}
	return putAttentionActivityTx(tx, activity)
}

// Native indexing can finish after a marker was committed. Merge inside its
// write transaction so a checkpoint based on an earlier snapshot cannot erase
// operator intent. No event replay is needed to repair the read result.
func mergeImportedAttentionTx(tx *bolt.Tx, itemID string, data []byte) error {
	var imported AttentionActivity
	if err := json.Unmarshal(data, &imported); err != nil {
		return fmt.Errorf("decode imported attention activity: %w", err)
	}
	if imported.WorkItemID != itemID {
		return fmt.Errorf("imported attention activity identity mismatch")
	}
	current, _, err := readAttentionActivityTx(tx, itemID)
	if err != nil {
		return err
	}
	imported.LastReadAt = laterTime(imported.LastReadAt, current.LastReadAt)
	imported.LastUnreadAt = laterTime(imported.LastUnreadAt, current.LastUnreadAt)
	imported.LastDeferredAt = laterTime(imported.LastDeferredAt, current.LastDeferredAt)
	imported.LastRequestedAt = laterTime(imported.LastRequestedAt, current.LastRequestedAt)
	imported.LastCompletedAt = laterTime(imported.LastCompletedAt, current.LastCompletedAt)
	return putAttentionActivityTx(tx, imported)
}

func (d *Database) AttentionActivity(itemID string) (AttentionActivityResult, error) {
	if !model.ValidID(itemID) {
		return AttentionActivityResult{}, fmt.Errorf("valid item_id is required")
	}
	result := AttentionActivityResult{Activity: AttentionActivity{WorkItemID: itemID}}
	err := d.db.View(func(tx *bolt.Tx) error {
		manifests := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection))
		if manifests == nil || manifests.Get([]byte(itemID)) == nil {
			return store.ErrNotFound
		}
		result.Projection = ProjectionMetadata{Revision: decodeUint64(tx.Bucket(bucketMeta).Get(keyGlobalSequence)), Source: "daemon.attention_activity", ObservedAt: time.Now().UTC(), Fresh: true}
		var err error
		result.Activity, result.Found, err = readAttentionActivityTx(tx, itemID)
		return err
	})
	return result, err
}
