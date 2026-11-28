package coordinator

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/regb/workitem/internal/apperr"
	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

// AccessProjection holds what items' agents may do to other items, as
// people granted it.
const AccessProjection = "access_entries"

// legacyDefaultAccessID names the entry installations used to start with:
// every item's agent could manage every item. Agents now start with access
// only to their own item and what it delegated, and what a person grants.
const legacyDefaultAccessID = "default-every-item"

// removeLegacyDefaultAccess deletes that entry, as wi recorded it, when the
// database opens.
func removeLegacyDefaultAccess(tx *bolt.Tx) error {
	bucket := tx.Bucket(bucketProjections).Bucket([]byte(AccessProjection))
	if bucket == nil {
		return nil
	}
	raw := bucket.Get([]byte(legacyDefaultAccessID))
	if raw == nil {
		return nil
	}
	var entry model.AccessEntry
	if err := json.Unmarshal(raw, &entry); err != nil || entry.GrantedBy != model.SystemActor {
		return nil
	}
	return bucket.Delete([]byte(legacyDefaultAccessID))
}

// AccessEntries lists every entry, oldest first.
func (d *Database) AccessEntries() ([]model.AccessEntry, error) {
	raw, err := d.ListProjection(AccessProjection)
	if err != nil {
		return nil, err
	}
	out := []model.AccessEntry{}
	for _, data := range raw {
		var entry model.AccessEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].GrantedAt.Equal(out[j].GrantedAt) {
			return out[i].GrantedAt.Before(out[j].GrantedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// GrantAccess records an entry. A subject has one entry per item: granting
// again replaces its level.
func (d *Database) GrantAccess(entry model.AccessEntry) (model.AccessEntry, error) {
	if err := entry.Validate(); err != nil {
		return model.AccessEntry{}, apperr.Invalidf("%v", err)
	}
	entry.ID = fmt.Sprintf("%s:%s", entry.Subject, entry.ItemID)
	err := d.updateNotify(func(tx *bolt.Tx) error {
		return putProjectionValue(tx, AccessProjection, entry.ID, entry)
	})
	return entry, err
}

// RevokeAccess removes an entry.
func (d *Database) RevokeAccess(id string) error {
	return d.updateNotify(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketProjections).Bucket([]byte(AccessProjection))
		if bucket == nil || bucket.Get([]byte(id)) == nil {
			return apperr.NotFoundf("access entry %s not found", id)
		}
		return bucket.Delete([]byte(id))
	})
}
