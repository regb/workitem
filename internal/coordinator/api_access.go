package coordinator

import (
	"encoding/json"
	bolt "go.etcd.io/bbolt"
)

// NotifyAll publishes changes to repository/profile state that affect many items.
func (d *Database) NotifyAll() { d.changes.recordAll() }

// updateNotify commits a write to state many items or clients show, such as
// agent profiles, schedules or a repository's environment, and wakes change
// readers once it is durable. Writes made through it cannot forget to.
func (d *Database) updateNotify(fn func(*bolt.Tx) error) error {
	err := d.db.Update(fn)
	if err == nil {
		d.changes.recordAll()
	}
	return err
}

// ChangesSnapshot returns an atomic change-feed snapshot and its wakeup signal.
func (d *Database) ChangesSnapshot(epoch string, after uint64) (ChangesResult, <-chan struct{}) {
	return d.changes.since(epoch, after)
}

// Database is the daemon's sole structured-state writer. It is exposed for
// in-process API composition, never to a remote client.
func (s *Server) Database() *Database { return s.database }

// CreatedItem recovers the durable result for an API creation idempotency key.
func (d *Database) CreatedItem(commandID string) (CreateItemResult, bool, error) {
	var result CreateItemResult
	found := false
	err := d.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketDomainCommands).Get([]byte(commandID))
		if data == nil {
			return nil
		}
		var outcome createCommandOutcome
		if err := json.Unmarshal(data, &outcome); err != nil {
			return err
		}
		result, found = outcome.Result, outcome.Command.ID != ""
		return nil
	})
	return result, found, err
}
