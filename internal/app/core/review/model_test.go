package review

import (
	"testing"
	"time"
)

func TestItemScopedReviewAndInformationalApproval(t *testing.T) {
	s := New("item", "repo")
	r := s.AddRevision(Revision{Head: "head", Commit: "snapshot", Tree: "tree", Ref: "private", Branch: "topic", Excluded: []Exclusion{{Path: "large", Reason: "size"}}})
	if r.ID != "r1" || r.Base != "head" {
		t.Fatal(r)
	}
	if _, err := s.Apply(Mutation{Kind: "intent", Revision: r.ID, Intent: "approve"}, time.Now()); err != nil || !s.Approved(r.ID) {
		t.Fatal("exclusions made approval unusable", err)
	}
	d, err := s.Apply(Mutation{Kind: "save_draft", Draft: Draft{Anchor: Anchor{Revision: r.ID}, Body: "Concern"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.Apply(Mutation{Kind: "publish", Revision: r.ID, DraftIDs: []string{d.Draft}}, time.Now())
	if err != nil || q.Round != "q1" || q.Threads[0] != "t1" {
		t.Fatal(q, err)
	}
	if _, err := s.Apply(Mutation{Kind: "reply", Revision: r.ID, Replies: []Reply{{Thread: "t1", Body: "Addressed", Addressed: true}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.Threads[0].Status() != "addressed" {
		t.Fatal("agent reply resolved reviewer concern")
	}
	if s.Base != "head" || s.Revisions[0].Base != "head" {
		t.Fatal("review base moved")
	}
}
