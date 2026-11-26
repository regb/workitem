package coordinator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	reviewcore "github.com/regb/workitem/internal/app/core/review"
	bolt "go.etcd.io/bbolt"
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

func TestReviewRecordCASAndRestartRetainCanonicalBodies(t *testing.T) {
	s, m := editFixture(t)
	ctx := context.Background()
	state, err := s.LoadReview(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.AddRevision(reviewcore.Revision{Head: "head", Commit: "snapshot", Tree: "tree", Branch: "branch", Ref: "private"})
	if _, err := state.Apply(reviewcore.Mutation{Kind: "save_draft", Draft: reviewcore.Draft{Anchor: reviewcore.Anchor{Revision: "r1"}, Body: "durable ordinary review content"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreReview(ctx, state, 0, "first", "draft_saved", map[string]string{"draft_id": "d1"}); err != nil {
		t.Fatal(err)
	}
	before, err := s.database.ListItemEvents(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StoreReview(ctx, state, 0, "stale", "draft_saved", nil); !errors.Is(err, ErrReviewConflict) {
		t.Fatalf("stale record write: %v", err)
	}
	after, err := s.database.ListItemEvents(m.ID)
	if err != nil || len(before) != len(after) {
		t.Fatal("stale write emitted a partial event", err)
	}
	root := filepath.Dir(s.database.path)
	if err := s.database.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restarted := &Server{database: db, native: s.native}
	restored, err := restarted.LoadReview(ctx, m.ID)
	if err != nil || restored.Version != 1 || restored.Drafts[0].Body != "durable ordinary review content" || restored.Base != "head" {
		t.Fatal("restart did not retain records", err)
	}
	// Corrupt records fail as ordinary storage errors, without consulting Git or
	// inventing repair/quarantine state.
	restored.RepositoryID = "other"
	if err := restarted.StoreReview(ctx, restored, 1, "wrong-owner", "draft_saved", nil); err == nil {
		t.Fatal("cross-item ownership accepted")
	}
}
