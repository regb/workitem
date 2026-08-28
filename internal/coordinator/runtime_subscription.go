package coordinator

import (
	"encoding/json"

	bolt "go.etcd.io/bbolt"
)

type RuntimeEventsRequest struct {
	ItemID        string `json:"item_id"`
	AfterSequence uint64 `json:"after_sequence,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	WaitMillis    int    `json:"wait_millis,omitempty"`
}

type RuntimeEventsResult struct {
	Events       []DomainEvent `json:"events"`
	LastSequence uint64        `json:"last_sequence"`
}

func (d *Database) RuntimeEvents(itemID string, afterSequence uint64, limit int) (RuntimeEventsResult, error) {
	if limit < 0 {
		limit = 0
	}
	result := RuntimeEventsResult{Events: []DomainEvent{}, LastSequence: afterSequence}
	err := d.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(bucketEvents).Cursor()
		start := encodeUint64(afterSequence + 1)
		if afterSequence == ^uint64(0) {
			return nil
		}
		for key, value := cursor.Seek(start); key != nil; key, value = cursor.Next() {
			var event DomainEvent
			if err := json.Unmarshal(value, &event); err != nil {
				return err
			}
			if event.ItemID != itemID || event.Actor != "runtime" || !allowedLiveRuntimeEvent(event.Type) {
				continue
			}
			result.Events = append(result.Events, event)
			result.LastSequence = event.Sequence
		}
		return nil
	})
	if err != nil {
		return RuntimeEventsResult{}, err
	}
	if limit > 0 && len(result.Events) > limit {
		result.Events = append([]DomainEvent(nil), result.Events[len(result.Events)-limit:]...)
		result.LastSequence = result.Events[len(result.Events)-1].Sequence
	}
	return result, nil
}
