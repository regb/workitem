package coordinator

import (
	"encoding/json"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

const apiJobsProjection = "api_jobs"
const APIJobHistoryLimit = 500

type apiJobSummary struct {
	Data       []byte    `json:"data"`
	FinishedAt time.Time `json:"finished_at"`
}

// APIJobs returns opaque protobuf summaries; the API owns their schema.
func (d *Database) APIJobs() ([][]byte, error) {
	var result [][]byte
	err := d.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketProjections).Bucket([]byte(apiJobsProjection))
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(_, raw []byte) error {
			var value apiJobSummary
			if err := json.Unmarshal(raw, &value); err != nil {
				return err
			}
			result = append(result, value.Data)
			return nil
		})
	})
	return result, err
}

// SaveAPIJob publishes a summary atomically and bounds finished history. Active
// records are never evicted. These are daemon-owned projections, not native text.
func (d *Database) SaveAPIJob(id string, data []byte, finishedAt time.Time) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(apiJobsProjection))
		if err != nil {
			return err
		}
		raw, err := json.Marshal(apiJobSummary{Data: data, FinishedAt: finishedAt})
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(id), raw); err != nil {
			return err
		}
		if finishedAt.IsZero() {
			return nil
		}
		type finished struct {
			id string
			at time.Time
		}
		var entries []finished
		if err := bucket.ForEach(func(key, raw []byte) error {
			var value apiJobSummary
			if err := json.Unmarshal(raw, &value); err != nil {
				return err
			}
			if !value.FinishedAt.IsZero() {
				entries = append(entries, finished{string(key), value.FinishedAt})
			}
			return nil
		}); err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].at.Equal(entries[j].at) {
				return entries[i].id > entries[j].id
			}
			return entries[i].at.After(entries[j].at)
		})
		for i := APIJobHistoryLimit; i < len(entries); i++ {
			if err := bucket.Delete([]byte(entries[i].id)); err != nil {
				return err
			}
		}
		return nil
	})
}
