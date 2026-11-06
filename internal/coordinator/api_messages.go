package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"time"
)

const apiMessagesProjection = "api_message_receipts"

// APIMessageReceipt records the external handoff, not native conversation text.
// A pending handoff is retried only with the same deterministic command ID on
// the same runtime; it must never be replayed onto a replacement runtime.
type APIMessageReceipt struct {
	ItemID     string    `json:"item_id"`
	MessageID  string    `json:"message_id"`
	RuntimeID  string    `json:"runtime_id"`
	Generation uint32    `json:"generation"`
	Delivered  bool      `json:"delivered"`
	CreatedAt  time.Time `json:"created_at"`
}

func apiMessageKey(itemID, messageID string) string {
	sum := sha256.Sum256([]byte(itemID + "\x00" + messageID))
	return hex.EncodeToString(sum[:])
}
func (d *Database) APIMessage(itemID, messageID string) (APIMessageReceipt, bool, error) {
	var value APIMessageReceipt
	found, err := d.ReadProjection(apiMessagesProjection, apiMessageKey(itemID, messageID), &value)
	return value, found, err
}
func (d *Database) RecordAPIMessage(value APIMessageReceipt) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(apiMessagesProjection))
		if err != nil {
			return err
		}
		key := []byte(apiMessageKey(value.ItemID, value.MessageID))
		if raw := bucket.Get(key); raw != nil {
			var old APIMessageReceipt
			if err := json.Unmarshal(raw, &old); err != nil {
				return err
			}
			if old.RuntimeID != value.RuntimeID || old.Generation != value.Generation {
				return fmt.Errorf("message was reserved on another agent generation")
			}
			if old.Delivered {
				return nil
			}
		}
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return bucket.Put(key, data)
	})
}
func (d *Database) ForgetAPIMessage(itemID, messageID string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketProjections).Bucket([]byte(apiMessagesProjection))
		if bucket == nil {
			return nil
		}
		return bucket.Delete([]byte(apiMessageKey(itemID, messageID)))
	})
}
