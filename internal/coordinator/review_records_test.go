package coordinator

import (
	"context"
	"errors"
	"fmt"
	reviewcore "github.com/regb/workitem/internal/app/core/review"
	bolt "go.etcd.io/bbolt"
	"testing"
)

func TestReviewContentIsCanonicalAndNotAProjection(t *testing.T) {
	s, m := editFixture(t)
	ctx := context.Background()
	state, _ := s.LoadReview(ctx, m.ID)
	state.Drafts = []reviewcore.Draft{{ID: "d1", Body: "only canonical copy"}}
	if err := s.StoreReview(ctx, state, 0, "first", "draft_saved", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.database.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketProjections).Bucket(bucketReviewRecords) != nil {
			t.Fatal("canonical content stored as projection")
		}
		cache, err := tx.Bucket(bucketProjections).CreateBucket(bucketReviewRecords)
		if err != nil {
			return err
		}
		if err := cache.Put([]byte(m.ID), []byte("stale cache")); err != nil {
			return err
		}
		return tx.Bucket(bucketProjections).DeleteBucket(bucketReviewRecords)
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadReview(ctx, m.ID)
	if err != nil || loaded.Drafts[0].Body != "only canonical copy" {
		t.Fatal("cache deletion lost canonical content", err)
	}
	if err := s.database.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketReviewRecords).Put([]byte(m.ID), []byte("malformed")) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadReview(ctx, m.ID); err == nil {
		t.Fatal("malformed canonical records were silently accepted")
	}
}
func TestExpiredReviewReceiptCannotReapplyAnOldOperation(t *testing.T) {
	s, m := editFixture(t)
	ctx := context.Background()
	state, _ := s.LoadReview(ctx, m.ID)
	for i := 0; i < 205; i++ {
		id := fmt.Sprintf("op-%d", i)
		state.RememberOperation(id, reviewcore.Receipt{Kind: "test", Digest: id})
		if err := s.StoreReview(ctx, state, state.Version, id, "test", nil); err != nil {
			t.Fatal(err)
		}
		state.Version++
	}
	loaded, err := s.LoadReview(ctx, m.ID)
	if err != nil || len(loaded.Operations) != 200 {
		t.Fatal("unbounded persisted receipts", err)
	}
	before, _ := s.database.ListItemEvents(m.ID)
	if err := s.StoreReview(ctx, loaded, loaded.Version, "op-0", "test", nil); !errors.Is(err, ErrReviewOperationExpired) {
		t.Fatalf("expired retry was reapplied: %v", err)
	}
	after, _ := s.database.ListItemEvents(m.ID)
	if len(before) != len(after) {
		t.Fatal("expired retry emitted another event")
	}
	current, _ := s.LoadReview(ctx, m.ID)
	if current.Version != loaded.Version {
		t.Fatal("expired retry changed records")
	}
}
