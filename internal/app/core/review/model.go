// Package review contains item-scoped review content and pure mutation rules.
package review

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Exclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type Revision struct {
	ID        string      `json:"id"`
	CaptureID string      `json:"capture_id"`
	Ref       string      `json:"ref"`
	Commit    string      `json:"commit"`
	Tree      string      `json:"tree"`
	Head      string      `json:"head"`
	Branch    string      `json:"branch"`
	Detached  bool        `json:"detached,omitempty"`
	Base      string      `json:"base"`
	CreatedAt time.Time   `json:"created_at"`
	Trigger   string      `json:"trigger"`
	Excluded  []Exclusion `json:"excluded,omitempty"`
}
type Anchor struct {
	Revision     string `json:"revision"`
	FromRevision string `json:"from_revision,omitempty"`
	Path         string `json:"path,omitempty"`
	Side         string `json:"side,omitempty"`
	StartLine    uint32 `json:"start_line,omitempty"`
	EndLine      uint32 `json:"end_line,omitempty"`
	BlobSHA256   string `json:"blob_sha256,omitempty"`
}
type Message struct {
	ID         string    `json:"id"`
	Role       string    `json:"role"`
	Body       string    `json:"body"`
	Revision   string    `json:"revision"`
	Round      string    `json:"round,omitempty"`
	Addressed  bool      `json:"addressed,omitempty"`
	Resolution string    `json:"resolution,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}
type Thread struct {
	ID       string    `json:"id"`
	Anchor   Anchor    `json:"anchor"`
	Messages []Message `json:"messages"`
}

func (t Thread) Status() string {
	status := "open"
	for _, m := range t.Messages {
		if m.Role == "reviewer" && m.Resolution != "" {
			status = m.Resolution
		}
		if m.Role == "agent" && m.Addressed && status != "resolved" && status != "withdrawn" {
			status = "addressed"
		}
	}
	return status
}

type Draft struct {
	ID         string `json:"id"`
	Thread     string `json:"thread,omitempty"`
	Anchor     Anchor `json:"anchor"`
	Body       string `json:"body"`
	Resolution string `json:"resolution,omitempty"`
}
type Round struct {
	ID           string     `json:"id"`
	Revision     string     `json:"revision"`
	FromRevision string     `json:"from_revision,omitempty"`
	Messages     []string   `json:"messages"`
	CreatedAt    time.Time  `json:"created_at"`
	NotifiedAt   *time.Time `json:"notified_at,omitempty"`
	CommandID    string     `json:"command_id,omitempty"`
}
type Approval struct {
	ID        string    `json:"id"`
	Revision  string    `json:"revision"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
type Merge struct {
	Revision  string    `json:"revision"`
	Commit    string    `json:"commit"`
	Tree      string    `json:"tree"`
	Target    string    `json:"target"`
	Before    string    `json:"before"`
	After     string    `json:"after"`
	CreatedAt time.Time `json:"created_at"`
}
type CaptureGap struct {
	ID        string    `json:"id"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}
type Receipt struct {
	Digest   string   `json:"digest"`
	Kind     string   `json:"kind"`
	Revision string   `json:"revision,omitempty"`
	Round    string   `json:"round,omitempty"`
	Draft    string   `json:"draft,omitempty"`
	Approval string   `json:"approval,omitempty"`
	Threads  []string `json:"threads,omitempty"`
}

// Version is an optimistic record revision, not a storage schema or Git tip.
type State struct {
	ItemID         string             `json:"item_id"`
	RepositoryID   string             `json:"repository_id"`
	Version        uint64             `json:"version"`
	Base           string             `json:"base,omitempty"`
	NextDraft      uint64             `json:"next_draft"`
	NextMessage    uint64             `json:"next_message"`
	Revisions      []Revision         `json:"revisions"`
	Threads        []Thread           `json:"threads"`
	Drafts         []Draft            `json:"drafts"`
	Rounds         []Round            `json:"rounds"`
	Approvals      []Approval         `json:"approvals"`
	Merges         []Merge            `json:"merges"`
	Gaps           []CaptureGap       `json:"gaps"`
	Operations     map[string]Receipt `json:"operations"`
	OperationOrder []string           `json:"operation_order"`
}

func New(item, repository string) State {
	return State{ItemID: item, RepositoryID: repository, Operations: map[string]Receipt{}}
}

const MaxReceipts = 200

func (s *State) RememberOperation(id string, r Receipt) {
	if s.Operations == nil {
		s.Operations = map[string]Receipt{}
	}
	if _, exists := s.Operations[id]; !exists {
		s.OperationOrder = append(s.OperationOrder, id)
	}
	s.Operations[id] = r
	for len(s.OperationOrder) > MaxReceipts {
		delete(s.Operations, s.OperationOrder[0])
		s.OperationOrder = s.OperationOrder[1:]
	}
}
func ValidBody(s string) bool {
	return utf8.ValidString(s) && len(s) <= 32<<10 && !strings.ContainsRune(s, 0)
}
func ValidRecordID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || len(id) <= len(prefix) || len(id) > 24 || id[len(prefix)] == '0' {
		return false
	}
	for _, r := range id[len(prefix):] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func (s State) Find(id string) (Revision, error) {
	for _, r := range s.Revisions {
		if r.ID == id {
			return r, nil
		}
	}
	return Revision{}, errors.New("review revision not found")
}
func (s *State) Thread(id string) (*Thread, error) {
	for i := range s.Threads {
		if s.Threads[i].ID == id {
			return &s.Threads[i], nil
		}
	}
	return nil, errors.New("review thread not found")
}
func (s *State) Round(id string) (*Round, error) {
	for i := range s.Rounds {
		if s.Rounds[i].ID == id {
			return &s.Rounds[i], nil
		}
	}
	return nil, errors.New("review round not found")
}
func (s *State) AddRevision(r Revision) Revision {
	if s.Base == "" {
		s.Base = r.Head
	}
	r.ID = fmt.Sprintf("r%d", len(s.Revisions)+1)
	r.Base = s.Base
	s.Revisions = append(s.Revisions, r)
	return r
}
func (s State) Approved(revision string) bool {
	approved := false
	for _, a := range s.Approvals {
		if a.Revision == revision {
			if a.Kind == "approve" {
				approved = true
			}
			if a.Kind == "withdraw_approval" {
				approved = false
			}
		}
	}
	return approved
}
func ValidDecision(kind string) bool {
	return kind == "approve" || kind == "withdraw_approval" || kind == "request_review" || kind == "request_changes"
}
func (s State) Validate(item, repository string) error {
	if s.ItemID != item || s.RepositoryID != repository || len(s.Revisions) > 100000 || len(s.Threads) > 100000 || len(s.Drafts) > 512 || len(s.Operations) > MaxReceipts || len(s.OperationOrder) > MaxReceipts {
		return errors.New("invalid review record ownership or size")
	}
	for i, r := range s.Revisions {
		if r.ID != fmt.Sprintf("r%d", i+1) || r.Commit == "" || r.Tree == "" || r.Head == "" || r.Base == "" || r.Ref == "" || r.Detached != (r.Branch == "") {
			return errors.New("invalid review revision")
		}
	}
	for _, t := range s.Threads {
		if !ValidRecordID(t.ID, "t") {
			return errors.New("invalid thread identity")
		}
		if _, err := s.Find(t.Anchor.Revision); err != nil {
			return err
		}
		for _, m := range t.Messages {
			if !ValidBody(m.Body) || m.Role != "agent" && m.Role != "reviewer" {
				return errors.New("invalid review message")
			}
		}
	}
	for _, d := range s.Drafts {
		if !ValidBody(d.Body) {
			return errors.New("invalid draft")
		}
	}
	for _, a := range s.Approvals {
		if !ValidDecision(a.Kind) || !ValidBody(a.Message) {
			return errors.New("invalid approval record")
		}
		if _, err := s.Find(a.Revision); err != nil {
			return err
		}
	}
	return nil
}
