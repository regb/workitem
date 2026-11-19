package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	reviewcore "github.com/regb/workitem/internal/app/core/review"
	bolt "go.etcd.io/bbolt"
	"time"
)

var ErrReviewConflict = errors.New("review records or checkout ownership changed")
var ErrReviewOperationExpired = errors.New("review retry receipt expired; inspect current records before starting a new operation")

// Review content is canonical item data, not an event-rebuildable projection.
func (d *Database) ReadReviewRecord(item string) (reviewcore.State, bool, error) {
	var state reviewcore.State
	found := false
	err := d.db.View(func(tx *bolt.Tx) error {
		body := tx.Bucket(bucketReviewRecords).Get([]byte(item))
		if body == nil {
			return nil
		}
		found = true
		return json.Unmarshal(body, &state)
	})
	return state, found, err
}
func (s *Server) LoadReview(ctx context.Context, item string) (reviewcore.State, error) {
	c, err := s.database.CanonicalManifest(item)
	if err != nil {
		return reviewcore.State{}, err
	}
	state, found, err := s.database.ReadReviewRecord(item)
	if err != nil {
		return state, err
	}
	if !found {
		state = reviewcore.New(item, c.Manifest.Repository.ID)
	}
	return state, state.Validate(item, c.Manifest.Repository.ID)
}

// StoreReview commits canonical content and compact events in one transaction.
// The coordinator does not capture snapshots or run Git for review.
func (s *Server) StoreReview(ctx context.Context, next reviewcore.State, expected uint64, operation, kind string, ids map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	current, err := s.LoadReview(ctx, next.ItemID)
	if err != nil {
		return err
	}
	if current.Version != expected {
		return ErrReviewConflict
	}
	if err := next.Validate(current.ItemID, current.RepositoryID); err != nil {
		return err
	}
	next.Version = expected + 1
	body, err := json.Marshal(next)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(ids)
	now := time.Now().UTC()
	id := "review:" + next.ItemID + ":" + operation
	result, err := s.database.commit(Command{ID: id, ProtocolVersion: ProtocolVersion, Type: "review." + kind, Actor: "wi", ItemID: next.ItemID, ReceivedAt: now}, []PendingEvent{{ID: id, ItemID: next.ItemID, Type: "review." + kind, Actor: "wi", Timestamp: now, Payload: payload}}, nil, func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketReviewRecords)
		var old reviewcore.State
		if data := bucket.Get([]byte(next.ItemID)); data != nil {
			if err := json.Unmarshal(data, &old); err != nil {
				return err
			}
		}
		if old.Version != expected {
			return ErrReviewConflict
		}
		return bucket.Put([]byte(next.ItemID), body)
	})
	if err == nil && result.Duplicate {
		return ErrReviewOperationExpired
	}
	return err
}
