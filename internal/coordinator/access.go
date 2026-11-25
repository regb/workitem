package coordinator

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/regb/workitem/internal/apperr"
	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

// AccessProjection holds what items' agents may do to other items. A
// default entry is an entry like any other, so what applies can always be
// listed and changed.
const AccessProjection = "access_entries"

// DefaultAccessID names the entry every installation starts with: every
// item's agent may manage every item, as before access lists existed. The
// operator lowers or removes it.
const DefaultAccessID = "default-every-item"

var keyAccessInitialized = []byte("access_initialized")

// ensureDefaultAccess records the default entry once per database, when it
// opens: removing it later sticks.
func ensureDefaultAccess(tx *bolt.Tx) error {
	meta := tx.Bucket(bucketMeta)
	if meta.Get(keyAccessInitialized) != nil {
		return nil
	}
	entry := model.AccessEntry{ID: DefaultAccessID, Subject: model.EveryItemPrincipal, ItemID: model.EveryItem, Level: model.AccessManage, GrantedBy: model.SystemActor, GrantedAt: time.Now().UTC()}
	if err := putProjectionValue(tx, AccessProjection, entry.ID, entry); err != nil {
		return err
	}
	return meta.Put(keyAccessInitialized, []byte("1"))
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
