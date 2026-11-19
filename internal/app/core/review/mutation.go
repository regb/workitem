package review

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Reply struct {
	Thread    string `json:"thread"`
	Body      string `json:"body"`
	Addressed bool   `json:"addressed"`
}
type Mutation struct {
	OperationID, Kind, Revision, FromRevision, Intent, Message string
	Draft                                                      Draft
	DraftIDs                                                   []string
	Replies                                                    []Reply
}

func ValidResolution(s string) bool {
	return s == "" || s == "open" || s == "resolved" || s == "withdrawn"
}
func (s State) ValidateReplies(replies []Reply) error {
	if len(replies) == 0 || len(replies) > 256 {
		return errors.New("reply batch must contain 1 to 256 replies")
	}
	seen := map[string]bool{}
	for _, r := range replies {
		if seen[r.Thread] || !ValidBody(r.Body) || strings.TrimSpace(r.Body) == "" && !r.Addressed {
			return errors.New("invalid or duplicate reply")
		}
		seen[r.Thread] = true
		t, err := s.Thread(r.Thread)
		if err != nil {
			return err
		}
		if t.Status() == "withdrawn" {
			return errors.New("cannot reply to withdrawn thread")
		}
	}
	return nil
}
func (s *State) Apply(r Mutation, now time.Time) (Receipt, error) {
	result := Receipt{Kind: r.Kind}
	switch r.Kind {
	case "save_draft":
		d := r.Draft
		if !ValidBody(d.Body) || !ValidResolution(d.Resolution) || strings.TrimSpace(d.Body) == "" && d.Resolution == "" {
			return result, errors.New("draft needs bounded text or reviewer resolution")
		}
		if _, err := s.Find(d.Anchor.Revision); err != nil {
			return result, err
		}
		if d.Thread != "" {
			if _, err := s.Thread(d.Thread); err != nil {
				return result, err
			}
		} else if d.Resolution != "" {
			return result, errors.New("new thread cannot resolve another concern")
		}
		if d.ID == "" {
			s.NextDraft++
			d.ID = fmt.Sprintf("d%d", s.NextDraft)
			s.Drafts = append(s.Drafts, d)
		} else {
			found := false
			for i, old := range s.Drafts {
				if old.ID == d.ID {
					s.Drafts[i] = d
					found = true
					break
				}
			}
			if !found {
				return result, errors.New("review draft not found")
			}
		}
		result.Draft = d.ID
	case "delete_draft":
		selected := map[string]bool{}
		for _, id := range r.DraftIDs {
			selected[id] = true
		}
		if len(selected) == 0 {
			return result, errors.New("draft identities required")
		}
		for id := range selected {
			found := false
			for _, d := range s.Drafts {
				if d.ID == id {
					found = true
					break
				}
			}
			if !found {
				return result, errors.New("review draft not found")
			}
		}
		kept := s.Drafts[:0]
		for _, d := range s.Drafts {
			if !selected[d.ID] {
				kept = append(kept, d)
			}
		}
		s.Drafts = kept
	case "publish":
		if _, err := s.Find(r.Revision); err != nil {
			return result, err
		}
		selected := map[string]bool{}
		for _, id := range r.DraftIDs {
			if selected[id] {
				return result, errors.New("duplicate draft identity")
			}
			selected[id] = true
		}
		if len(selected) == 0 {
			return result, errors.New("select exact drafts to publish")
		}
		var drafts []Draft
		for _, d := range s.Drafts {
			if selected[d.ID] {
				if d.Anchor.Revision != r.Revision || d.Anchor.FromRevision != r.FromRevision {
					return result, errors.New("draft comparison differs from published round")
				}
				drafts = append(drafts, d)
			}
		}
		if len(drafts) != len(selected) {
			return result, errors.New("review draft not found")
		}
		q := Round{ID: fmt.Sprintf("q%d", len(s.Rounds)+1), Revision: r.Revision, FromRevision: r.FromRevision, CreatedAt: now}
		for _, d := range drafts {
			var t *Thread
			if d.Thread == "" {
				s.Threads = append(s.Threads, Thread{ID: fmt.Sprintf("t%d", len(s.Threads)+1), Anchor: d.Anchor})
				t = &s.Threads[len(s.Threads)-1]
			} else {
				var err error
				t, err = s.Thread(d.Thread)
				if err != nil {
					return result, err
				}
			}
			s.NextMessage++
			m := Message{ID: fmt.Sprintf("m%d", s.NextMessage), Role: "reviewer", Body: d.Body, Revision: r.Revision, Round: q.ID, Resolution: d.Resolution, CreatedAt: now}
			t.Messages = append(t.Messages, m)
			q.Messages = append(q.Messages, m.ID)
			result.Threads = append(result.Threads, t.ID)
		}
		kept := s.Drafts[:0]
		for _, d := range s.Drafts {
			if !selected[d.ID] {
				kept = append(kept, d)
			}
		}
		s.Drafts = kept
		s.Rounds = append(s.Rounds, q)
		result.Round = q.ID
	case "reply":
		if err := s.ValidateReplies(r.Replies); err != nil {
			return result, err
		}
		if _, err := s.Find(r.Revision); err != nil {
			return result, err
		}
		for _, rply := range r.Replies {
			t, _ := s.Thread(rply.Thread)
			s.NextMessage++
			t.Messages = append(t.Messages, Message{ID: fmt.Sprintf("m%d", s.NextMessage), Role: "agent", Body: rply.Body, Revision: r.Revision, Addressed: rply.Addressed, CreatedAt: now})
			result.Threads = append(result.Threads, t.ID)
		}
		result.Revision = r.Revision
	case "intent":
		if !ValidDecision(r.Intent) || !ValidBody(r.Message) {
			return result, errors.New("invalid approval or review intent")
		}
		if _, err := s.Find(r.Revision); err != nil {
			return result, err
		}
		a := Approval{ID: fmt.Sprintf("a%d", len(s.Approvals)+1), Revision: r.Revision, Kind: r.Intent, Message: r.Message, CreatedAt: now}
		s.Approvals = append(s.Approvals, a)
		result.Approval = a.ID
	default:
		return result, errors.New("invalid review operation")
	}
	return result, nil
}
