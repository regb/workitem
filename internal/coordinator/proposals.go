package coordinator

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/regb/workitem/internal/apperr"
	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

// ProposalProjection holds the items agents asked their user to file. A
// proposal is pending input, not an item: its description is kept here
// until it is filed and becomes the new item's description.
const ProposalProjection = "proposals"

// Proposal states.
const (
	ProposalPending   = "pending"
	ProposalFiled     = "filed"
	ProposalDismissed = "dismissed"
)

// MaxProposalDescription bounds a proposal's description.
const MaxProposalDescription = 64 << 10

// Proposal is an item an agent asked its user to file.
type Proposal struct {
	ID           string     `json:"id"`
	ItemID       string     `json:"item_id"`
	Proposer     string     `json:"proposer"`
	RepositoryID string     `json:"repository_id"`
	Title        string     `json:"title"`
	Description  string     `json:"description,omitempty"`
	Labels       []string   `json:"labels,omitempty"`
	State        string     `json:"state"`
	CreatedAt    time.Time  `json:"created_at"`
	FiledItemID  string     `json:"filed_item_id,omitempty"`
	DecidedBy    string     `json:"decided_by,omitempty"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	Reason       string     `json:"reason,omitempty"`
}

// SaveProposal records a new pending proposal. Recording one with an ID
// already used returns the existing proposal, so a retried request records
// it once; created reports whether this call recorded it.
func (d *Database) SaveProposal(value Proposal) (saved Proposal, created bool, err error) {
	if value.ID == "" || !model.ValidID(value.ItemID) || !model.ValidID(value.RepositoryID) || !model.ValidPrincipal(value.Proposer) {
		return Proposal{}, false, errors.New("a proposal needs an id, its item, a repository and a proposer")
	}
	if value.Title == "" || len(value.Title) > 512 || len(value.Description) > MaxProposalDescription {
		return Proposal{}, false, apperr.Invalidf("a proposal needs a title of up to 512 bytes and a description of up to %d", MaxProposalDescription)
	}
	value.State = ProposalPending
	err = d.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(ProposalProjection))
		if err != nil {
			return err
		}
		if raw := bucket.Get([]byte(value.ID)); raw != nil {
			return json.Unmarshal(raw, &saved)
		}
		saved, created = value, true
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(value.ID), data)
	})
	return saved, created, err
}

// GetProposal reads one proposal.
func (d *Database) GetProposal(id string) (Proposal, error) {
	var value Proposal
	found, err := d.ReadProjection(ProposalProjection, id, &value)
	if err == nil && !found {
		err = apperr.NotFoundf("proposal %s not found", id)
	}
	return value, err
}

// ListProposals reads proposals, newest first: an item's, or every item's.
func (d *Database) ListProposals(itemID string, pendingOnly bool) ([]Proposal, error) {
	raw, err := d.ListProjection(ProposalProjection)
	if err != nil {
		return nil, err
	}
	out := []Proposal{}
	for _, data := range raw {
		var value Proposal
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		if itemID != "" && value.ItemID != itemID || pendingOnly && value.State != ProposalPending {
			continue
		}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// DecideProposal files or dismisses a pending proposal, once. Filing it
// again with the same item returns it unchanged, so a retried file is safe.
func (d *Database) DecideProposal(id, state, filedItemID, decidedBy, reason string, at time.Time) (Proposal, error) {
	if state != ProposalFiled && state != ProposalDismissed {
		return Proposal{}, fmt.Errorf("invalid proposal decision %q", state)
	}
	var value Proposal
	err := d.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketProjections).Bucket([]byte(ProposalProjection))
		var raw []byte
		if bucket != nil {
			raw = bucket.Get([]byte(id))
		}
		if raw == nil {
			return apperr.NotFoundf("proposal %s not found", id)
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		if value.State == state && value.FiledItemID == filedItemID {
			return nil
		}
		if value.State != ProposalPending {
			return fmt.Errorf("proposal %s was already %s", id, value.State)
		}
		value.State, value.FiledItemID, value.DecidedBy, value.Reason = state, filedItemID, decidedBy, reason
		value.DecidedAt = &at
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(id), data)
	})
	return value, err
}
