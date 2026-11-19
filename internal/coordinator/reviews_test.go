package coordinator

import (
	"context"
	"errors"
	reviewcore "github.com/regb/workitem/internal/app/core/review"
	"path/filepath"
	"testing"
	"time"
)

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
