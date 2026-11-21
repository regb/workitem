package coordinator

import (
	"encoding/json"
	"fmt"
	"github.com/regb/workitem/internal/apperr"
	"sort"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

const maxSchedules = 200

// ErrScheduleChanged means a schedule was edited since the caller read it.
var ErrScheduleChanged = apperr.Stalef("schedule changed since it was read; reload and try again")

func scheduleBucket(tx *bolt.Tx) (*bolt.Bucket, error) {
	if bucket := tx.Bucket(bucketSchedules); bucket != nil {
		return bucket, nil
	}
	if !tx.Writable() {
		return nil, nil
	}
	return tx.CreateBucketIfNotExists(bucketSchedules)
}

func readSchedule(bucket *bolt.Bucket, id string) (model.Schedule, bool, error) {
	var value model.Schedule
	raw := bucket.Get([]byte(id))
	if raw == nil {
		return value, false, nil
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, false, fmt.Errorf("decode schedule %s: %w", id, err)
	}
	return value, true, nil
}

func writeSchedule(bucket *bolt.Bucket, value model.Schedule) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put([]byte(value.ID), raw)
}

// ListSchedules returns every schedule, oldest first.
func (d *Database) ListSchedules() ([]model.Schedule, error) {
	out := []model.Schedule{}
	err := d.db.View(func(tx *bolt.Tx) error {
		bucket, err := scheduleBucket(tx)
		if err != nil || bucket == nil {
			return err
		}
		return bucket.ForEach(func(key, raw []byte) error {
			var value model.Schedule
			if err := json.Unmarshal(raw, &value); err != nil {
				return fmt.Errorf("decode schedule %s: %w", key, err)
			}
			out = append(out, value)
			return nil
		})
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, err
}

// Schedule reads one schedule.
func (d *Database) Schedule(id string) (model.Schedule, bool, error) {
	var value model.Schedule
	var found bool
	err := d.db.View(func(tx *bolt.Tx) error {
		bucket, err := scheduleBucket(tx)
		if err != nil || bucket == nil {
			return err
		}
		value, found, err = readSchedule(bucket, id)
		return err
	})
	return value, found, err
}

// PutSchedule creates a schedule (empty ID) or replaces the fields a person
// sets on an existing one. expectedRevision, when nonzero, fences an edit.
// Changing the schedule computes its next firing from now.
func (d *Database) PutSchedule(value model.Schedule, expectedRevision uint32, now time.Time) (model.Schedule, error) {
	if err := value.Validate(); err != nil {
		return model.Schedule{}, err
	}
	labels, _ := model.NormalizeLabels(value.Labels)
	value.Labels = labels
	var result model.Schedule
	err := d.updateNotify(func(tx *bolt.Tx) error {
		bucket, err := scheduleBucket(tx)
		if err != nil {
			return err
		}
		if value.ID == "" {
			if bucket.Stats().KeyN >= maxSchedules {
				return fmt.Errorf("at most %d schedules", maxSchedules)
			}
			id, err := model.NewID()
			if err != nil {
				return err
			}
			value.ID, value.CreatedAt, value.Revision = id, now, 0
			value.LastRunAt, value.LastItemID, value.LastError = nil, "", ""
		} else {
			current, found, err := readSchedule(bucket, value.ID)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("schedule %s not found", value.ID)
			}
			if expectedRevision != 0 && current.Revision != expectedRevision {
				return ErrScheduleChanged
			}
			value.CreatedAt, value.Revision = current.CreatedAt, current.Revision
			value.LastRunAt, value.LastItemID, value.LastError = current.LastRunAt, current.LastItemID, current.LastError
		}
		value.Revision++
		value.UpdatedAt = now
		value.NextRunAt = value.Next(now)
		result = value
		return writeSchedule(bucket, value)
	})
	return result, err
}

// DeleteSchedule removes a schedule. Items it started are unaffected.
func (d *Database) DeleteSchedule(id string) error {
	return d.updateNotify(func(tx *bolt.Tx) error {
		bucket, err := scheduleBucket(tx)
		if err != nil {
			return err
		}
		if _, found, err := readSchedule(bucket, id); err != nil {
			return err
		} else if !found {
			return fmt.Errorf("schedule %s not found", id)
		}
		return bucket.Delete([]byte(id))
	})
}

// AdvanceSchedule moves a schedule past the firing due at due, to its next
// firing after now. It reports false when another caller already advanced
// it, so each firing is taken once. It does not change the revision: it
// is bookkeeping, not an edit.
func (d *Database) AdvanceSchedule(id string, due, now time.Time) (model.Schedule, bool, error) {
	var result model.Schedule
	taken := false
	err := d.updateNotify(func(tx *bolt.Tx) error {
		bucket, err := scheduleBucket(tx)
		if err != nil {
			return err
		}
		current, found, err := readSchedule(bucket, id)
		if err != nil || !found || current.NextRunAt == nil || !current.NextRunAt.Equal(due) {
			result = current
			return err
		}
		current.NextRunAt = current.Next(now)
		result, taken = current, true
		return writeSchedule(bucket, current)
	})
	return result, taken, err
}

// RecordScheduleRun records the outcome of a firing.
func (d *Database) RecordScheduleRun(id string, at time.Time, itemID, failure string) error {
	return d.updateNotify(func(tx *bolt.Tx) error {
		bucket, err := scheduleBucket(tx)
		if err != nil {
			return err
		}
		current, found, err := readSchedule(bucket, id)
		if err != nil || !found {
			return err
		}
		at = at.UTC()
		current.LastRunAt, current.LastItemID, current.LastError = &at, itemID, failure
		return writeSchedule(bucket, current)
	})
}
